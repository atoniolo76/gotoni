#!/usr/bin/env python3
"""Download chunks of the AllenAI WildChat-1M dataset locally."""

import argparse
import json
import time
from datetime import datetime
from datasets import load_dataset
from pathlib import Path


def json_serializer(obj):
    """Custom JSON serializer for datetime objects."""
    if isinstance(obj, datetime):
        return obj.isoformat()
    raise TypeError(f"Object of type {type(obj)} is not JSON serializable")


def download_wildchat_chunk(output_file: str, split: str = "train", start_idx: int = 0, count: int = 10000):
    """Download a chunk of the WildChat dataset and save to JSONL format."""

    print("🚀 Downloading AllenAI WildChat-1M dataset chunk...")
    print(f"📁 Output file: {output_file}")
    print(f"🔄 Split: {split}")
    print(f"📊 Start index: {start_idx:,}")
    print(f"📊 Count: {count:,}")
    print("=" * 60)

    # Start timing
    start_time = time.time()

    # Load the dataset chunk
    print("⏳ Loading dataset chunk from Hugging Face...")
    end_idx = start_idx + count - 1
    slice_str = f"{start_idx}:{end_idx}"
    dataset = load_dataset("allenai/WildChat-1M", split=f"{split}[{slice_str}]")

    print(f"✅ Dataset chunk loaded! Contains {len(dataset)} conversations")
    print(f"📊 Dataset size: {len(dataset):,}")

    # Save to JSONL format
    print(f"💾 Saving to {output_file}...")

    with open(output_file, 'w', encoding='utf-8') as f:
        for i, item in enumerate(dataset):
            # Convert the item to a dict
            item_dict = dict(item)

            # Write each conversation as a JSON line
            json.dump(item_dict, f, ensure_ascii=False, default=json_serializer)
            f.write('\n')

            # Progress update every 1000 conversations
            if (i + 1) % 1000 == 0:
                print(f"  📊 Saved {i + 1:,} conversations...")

    # Final stats
    end_time = time.time()
    total_time = end_time - start_time

    file_size_mb = Path(output_file).stat().st_size / (1024 * 1024)

    print("=" * 60)
    print("✅ Download complete!")
    print(f"⏱️  Total time: {total_time:.2f} seconds")
    print(f"💾 File size: {file_size_mb:.1f} MB")
    print("🎉 Dataset chunk ready for use!")


def main():
    parser = argparse.ArgumentParser(
        description="Download chunks of AllenAI WildChat-1M dataset to local JSONL file"
    )
    parser.add_argument(
        "--output",
        "-o",
        default="wildchat_chunk.jsonl",
        help="Output JSONL file path (default: wildchat_chunk.jsonl)"
    )
    parser.add_argument(
        "--split",
        default="train",
        choices=["train", "test"],
        help="Dataset split to download (default: train)"
    )
    parser.add_argument(
        "--start",
        "-s",
        type=int,
        default=0,
        help="Start index in dataset (default: 0)"
    )
    parser.add_argument(
        "--count",
        "-c",
        type=int,
        default=10000,
        help="Number of conversations to download (default: 10000)"
    )
    parser.add_argument(
        "--force",
        "-f",
        action="store_true",
        help="Overwrite output file if it exists"
    )

    args = parser.parse_args()

    # Check if output file already exists
    if Path(args.output).exists() and not args.force:
        print(f"⚠️  Warning: {args.output} already exists!")
        print("💡 Use --force or -f to overwrite existing file")
        print("❌ Aborted.")
        return

    download_wildchat_chunk(args.output, args.split, args.start, args.count)


if __name__ == "__main__":
    main()