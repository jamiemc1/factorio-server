import { S3 } from 'ultralight-s3';

const s3 = new S3({
  endpoint: process.env.AWS_ENDPOINT_URL_S3,
  accessKeyId: process.env.AWS_ACCESS_KEY_ID,
  secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  bucketName: process.env.AWS_BUCKET_NAME,
  region: 'auto',
});
var date = new Date();

var local_filename = `/factorio/saves/${process.env.SAVE_NAME}.zip`
var remote_filename =  `${process.env.SAVE_NAME}.zip`
console.log(`Downloading ${process.env.AWS_BUCKET_NAME}/${remote_filename}`)
await s3.get(remote_filename);

var local_filename = `/factorio/saves/${process.env.SAVE_NAME}.zip`
var remote_filename =  `${process.env.SAVE_NAME}_${date.toISOString()}.zip`
console.log(`Downloading ${process.env.AWS_BUCKET_NAME}/${remote_filename}`)
await s3.get(remote_filename);

var local_filename = `/factorio/saves/_autosave1.zip`
var remote_filename =  `_autosave1.zip`
console.log(`Downloading ${process.env.AWS_BUCKET_NAME}/${remote_filename}`)
await s3.get(remote_filename);
