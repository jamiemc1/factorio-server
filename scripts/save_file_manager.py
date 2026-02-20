import os
import sys
import fsspec
from argparse import ArgumentParser
from s3fs import S3FileSystem
from datetime import datetime, timezone


def parse_arguments() -> ArgumentParser:
    parser = ArgumentParser(
        "Factorio save file manager",
    )
    exclusive_group = parser.add_mutually_exclusive_group()
    exclusive_group.add_argument(
        "--upload",
        default=False,
        action="store_true",
    )
    exclusive_group.add_argument(
        "--download",
        default=False,
        action="store_true",
    )

    return parser


def log(message: str) -> None:
    timestamp = datetime.now(tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    print(f"[{timestamp}] {message}")


def upload_file(fs: S3FileSystem, source_url: str, destination_url: str) -> None:
    with fsspec.open(source_url, mode="rb") as source:
        with fs.open(destination_url, mode="wb") as destination:
            destination.write(source.read())


def download_file(fs: S3FileSystem, source_url: str, destination_url: str) -> None:
    with fs.open(source_url, mode="rb") as source:
        with fsspec.open(destination_url, mode="wb") as destination:
            destination.write(source.read())


def main() -> None:
    args = parse_arguments().parse_args()

    key = os.environ.get("AWS_ACCESS_KEY_ID")
    secret = os.environ.get("AWS_SECRET_ACCESS_KEY")
    endpoint_url = os.environ.get("AWS_ENDPOINT_URL_S3")

    if not all([key, secret, endpoint_url]):
        missing = [
            name for name, val in [
                ("AWS_ACCESS_KEY_ID", key),
                ("AWS_SECRET_ACCESS_KEY", secret),
                ("AWS_ENDPOINT_URL_S3", endpoint_url),
            ]
            if not val
        ]
        log(f"FATAL: Missing environment variables: {', '.join(missing)}")
        sys.exit(1)

    fs = S3FileSystem(
        key=key,
        secret=secret,
        endpoint_url=endpoint_url,
    )

    bucket_name = os.environ.get("AWS_BUCKET_NAME")
    save_name = os.environ.get("SAVE_NAME", "_autosave1")

    if not bucket_name:
        log("FATAL: Missing AWS_BUCKET_NAME")
        sys.exit(1)

    log(f"Using bucket={bucket_name}, save_name={save_name}")

    if args.upload:
        had_failure = False

        source = f"/factorio/saves/{save_name}.zip"
        destination = f"{bucket_name}/{save_name}.zip"
        log(f"Uploading {source} -> s3://{destination}")
        try:
            upload_file(fs, source, destination)
            log(f"Uploaded {destination}")
        except FileNotFoundError:
            log(f"FAILED: Source file not found: {source}")
            had_failure = True
        except Exception as error:
            log(f"FAILED: Upload to {destination}: {type(error).__name__}: {error}")
            had_failure = True

        timestamp_string = datetime.now(tz=timezone.utc).strftime("%Y%m%d_%H%M%S")
        destination_timestamped = f"{bucket_name}/{save_name}_{timestamp_string}.zip"
        log(f"Uploading timestamped backup -> s3://{destination_timestamped}")
        try:
            upload_file(fs, source, destination_timestamped)
            log(f"Uploaded {destination_timestamped}")
        except FileNotFoundError:
            log(f"FAILED: Source file not found: {source}")
            had_failure = True
        except Exception as error:
            log(f"FAILED: Upload to {destination_timestamped}: {type(error).__name__}: {error}")
            had_failure = True

        source_autosave = "/factorio/saves/_autosave1.zip"
        destination_autosave = f"{bucket_name}/_autosave1.zip"
        if source_autosave != source:
            log(f"Uploading {source_autosave} -> s3://{destination_autosave}")
            try:
                upload_file(fs, source_autosave, destination_autosave)
                log(f"Uploaded {destination_autosave}")
            except FileNotFoundError:
                log(f"FAILED: Source file not found: {source_autosave}")
                had_failure = True
            except Exception as error:
                log(f"FAILED: Upload to {destination_autosave}: {type(error).__name__}: {error}")
                had_failure = True

        if had_failure:
            log("WARNING: One or more uploads failed")
            sys.exit(1)

        log("All uploads completed successfully")

    if args.download:
        source = f"{bucket_name}/{save_name}.zip"
        destination = f"/factorio/saves/{save_name}.zip"
        log(f"Downloading s3://{source} -> {destination}")
        try:
            download_file(fs, source, destination)
            log(f"Downloaded {destination}")
        except FileNotFoundError:
            log(f"FAILED: Source not found in S3: {source}")

        source = f"{bucket_name}/_autosave1.zip"
        destination_autosave = "/factorio/saves/_autosave1.zip"
        log(f"Downloading s3://{source} -> {destination_autosave}")
        download_file(fs, source, destination_autosave)
        log(f"Downloaded {destination_autosave}")


if __name__ == "__main__":
    main()
