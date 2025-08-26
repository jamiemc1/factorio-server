import os
import fsspec
from argparse import ArgumentParser
from pathlib import Path
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
    fs = S3FileSystem(
        key=key,
        secret=secret,
        endpoint_url=endpoint_url,
    )

    bucket_name = os.environ.get("AWS_BUCKET_NAME")
    save_name = os.environ.get("SAVE_NAME")

    if args.upload:
        source = f"/factorio/saves/{save_name}.zip"
        destination = f"{bucket_name}/{save_name}.zip"
        try:
            upload_file(fs, source, destination)
        except FileNotFoundError:
            destination = ""

        timestamp_string = datetime.now(tz=timezone.utc).strftime("%Y%m%d_%H%M%S")
        destination_timestamped = f"{bucket_name}/{save_name}_{timestamp_string}.zip"
        try:
            upload_file(fs, source, destination_timestamped)
        except FileNotFoundError:
            destination_timestamped = ""

        source_autosave = "/factorio/saves/_autosave1.zip"
        destination_autosave = f"{bucket_name}/_autosave1.zip"
        try:
            upload_file(fs, source_autosave, destination_autosave)
        except FileNotFoundError:
            destination_autosave = ""

        print(f"Files uploaded\n{destination}\n{destination_timestamped}\n{destination_autosave}")
    
    if args.download:
        source = f"{bucket_name}/{save_name}.zip"
        destination = f"/factorio/saves/{save_name}.zip"
        try:
            download_file(fs, source, destination)
        except FileNotFoundError:
            destination = ""

        source = f"{bucket_name}/_autosave1.zip"
        destination_autosave = "/factorio/saves/_autosave1.zip"
        download_file(fs, source, destination_autosave)


        print(f"Files downloaded\n{destination}\n{destination_autosave}")

    return None


if __name__ == "__main__":
    main()

