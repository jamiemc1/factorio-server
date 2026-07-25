// Package s3 wraps an S3-compatible object store (Tigris on Fly, etc) for
// uploading and downloading Factorio saves and caching mod zips. It preserves
// the exact object keys the previous Python save_file_manager used.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Store is a thin wrapper over the S3 API bound to a single bucket.
type Store struct {
	client *s3.Client
	bucket string
}

// New builds a Store against a custom S3-compatible endpoint using static
// credentials and path-style addressing (required by most S3-compatible
// providers).
func New(accessKey, secretKey, endpoint, bucket string) *Store {
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true,
	})
	return &Store{client: client, bucket: bucket}
}

// Exists reports whether an object exists at key.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// UploadFile uploads a local file to key.
func (s *Store) UploadFile(ctx context.Context, localPath, key string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.Upload(ctx, key, f)
}

// Upload streams r to key.
func (s *Store) Upload(ctx context.Context, key string, r io.Reader) error {
	// The S3 PutObject signer needs a seekable body or a known length; buffer
	// through a temp reader if r is not seekable. Save/mod zips are modest, so
	// reading into memory is acceptable and keeps signing simple.
	body, ok := r.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		body = newBytesReadSeeker(data)
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	return err
}

// DownloadFile downloads key to localPath, creating parent dirs as needed.
func (s *Store) DownloadFile(ctx context.Context, key, localPath string) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return err
	}
	defer out.Body.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, out.Body)
	return err
}

// Download returns a reader for key. The caller must close it.
func (s *Store) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// IsNotFound reports whether err is an S3 "object does not exist" error.
func IsNotFound(err error) bool { return isNotFound(err) }

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	// HeadObject returns a bare 404 with no typed error; match on code.
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

// TimestampSuffix returns the UTC timestamp string used for backup keys,
// matching the previous "%Y%m%d_%H%M%S" format.
func TimestampSuffix() string {
	return time.Now().UTC().Format("20060102_150405")
}

// bytesReadSeeker adapts a byte slice to io.ReadSeeker without pulling in bytes
// import churn at call sites.
type bytesReadSeeker struct {
	data []byte
	off  int64
}

func newBytesReadSeeker(data []byte) *bytesReadSeeker { return &bytesReadSeeker{data: data} }

func (b *bytesReadSeeker) Read(p []byte) (int, error) {
	if b.off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.off:])
	b.off += int64(n)
	return n, nil
}

func (b *bytesReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.off + offset
	case io.SeekEnd:
		abs = int64(len(b.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative position")
	}
	b.off = abs
	return abs, nil
}
