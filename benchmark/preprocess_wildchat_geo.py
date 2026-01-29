"""Preprocess WildChat for GuideLLM with location lookup table.

This script:
1. Loads WildChat-1M dataset from HuggingFace
2. Extracts first user message as prompt for each conversation
3. Computes a hash for each prompt
4. Creates a lookup table mapping hash -> user location (lat, lon)
5. Outputs:
   - wildchat_guidellm.jsonl: Prompts for GuideLLM
   - wildchat_location_lookup.json: Hash -> location mapping for geo-proxy
"""
import argparse
import hashlib
import json
from typing import Dict, Optional, Tuple

import datasets

# =============================================================================
# Location Coordinates (copied from wildchat2.py)
# =============================================================================

LOCATION_COORDS: Dict[str, Tuple[float, float]] = {
    # North America
    "United States": (39.8283, -98.5795),
    "Canada": (56.1304, -106.3468),
    "Mexico": (23.6345, -102.5528),
    # US States (East)
    "Virginia": (37.4316, -78.6569),
    "North Carolina": (35.7596, -79.0193),
    "South Carolina": (33.8361, -81.1637),
    "Georgia": (32.1656, -82.9001),
    "Florida": (27.6648, -81.5158),
    "Tennessee": (35.5175, -86.5804),
    "Kentucky": (37.8393, -84.2700),
    "West Virginia": (38.5976, -80.4549),
    "Maryland": (39.0458, -76.6413),
    "Delaware": (38.9108, -75.5277),
    "Pennsylvania": (41.2033, -77.1945),
    "New York": (40.7128, -74.0060),
    "New Jersey": (40.0583, -74.4057),
    "Connecticut": (41.6032, -73.0877),
    "Massachusetts": (42.4072, -71.3824),
    "Ohio": (40.4173, -82.9071),
    "Indiana": (40.2672, -86.1349),
    "Illinois": (40.6331, -89.3985),
    "Michigan": (44.3148, -85.6024),
    "Wisconsin": (43.7844, -88.7879),
    "Minnesota": (46.7296, -94.6859),
    "Iowa": (41.8780, -93.0977),
    "Missouri": (37.9643, -91.8318),
    "Arkansas": (35.2010, -91.8318),
    # US States (West)
    "California": (36.7783, -119.4179),
    "Washington": (47.7511, -120.7401),
    "Oregon": (43.8041, -120.5542),
    "Nevada": (38.8026, -116.4194),
    "Arizona": (34.0489, -111.0937),
    "Colorado": (39.5501, -105.7821),
    "Texas": (31.9686, -99.9018),
    # Europe
    "United Kingdom": (55.3781, -3.4360),
    "Ireland": (53.1424, -7.6921),
    "France": (46.2276, 2.2137),
    "Germany": (51.1657, 10.4515),
    "Spain": (40.4637, -3.7492),
    "Italy": (41.8719, 12.5674),
    "Portugal": (39.3999, -8.2245),
    "Netherlands": (52.1326, 5.2913),
    "Belgium": (50.5039, 4.4699),
    "Switzerland": (46.8182, 8.2275),
    "Austria": (47.5162, 14.5501),
    "Poland": (51.9194, 19.1451),
    "Czech Republic": (49.8175, 15.4730),
    "Hungary": (47.1625, 19.5033),
    "Sweden": (60.1282, 18.6435),
    "Norway": (60.4720, 8.4689),
    "Finland": (61.9241, 25.7482),
    "Denmark": (56.2639, 9.5018),
    "Greece": (39.0742, 21.8243),
    "Romania": (45.9432, 24.9668),
    "Bulgaria": (42.7339, 25.4858),
    "Croatia": (45.1000, 15.2000),
    "Slovakia": (48.6690, 19.6990),
    "Slovenia": (46.1512, 14.9955),
    "Estonia": (58.5953, 25.0136),
    "Latvia": (56.8796, 24.6032),
    "Lithuania": (55.1694, 23.8813),
    "Ukraine": (48.3794, 31.1656),
    "Russia": (61.5240, 105.3188),
    # Middle East
    "Israel": (31.0461, 34.8516),
    "Turkey": (38.9637, 35.2433),
    "Saudi Arabia": (23.8859, 45.0792),
    "United Arab Emirates": (23.4241, 53.8478),
    "Iran": (32.4279, 53.6880),
    "Egypt": (26.8206, 30.8025),
    # Asia
    "Japan": (36.2048, 138.2529),
    "South Korea": (35.9078, 127.7669),
    "China": (35.8617, 104.1954),
    "India": (20.5937, 78.9629),
    "Pakistan": (30.3753, 69.3451),
    "Bangladesh": (23.6850, 90.3563),
    "Vietnam": (14.0583, 108.2772),
    "Thailand": (15.8700, 100.9925),
    "Indonesia": (-0.7893, 113.9213),
    "Malaysia": (4.2105, 101.9758),
    "Philippines": (12.8797, 121.7740),
    "Singapore": (1.3521, 103.8198),
    "Taiwan": (23.6978, 120.9605),
    "Hong Kong": (22.3193, 114.1694),
    # Oceania
    "Australia": (-25.2744, 133.7751),
    "New Zealand": (-40.9006, 174.8860),
    # South America
    "Brazil": (-14.2350, -51.9253),
    "Argentina": (-38.4161, -63.6167),
    "Chile": (-35.6751, -71.5430),
    "Colombia": (4.5709, -74.2973),
    "Peru": (-9.1900, -75.0152),
    # Africa
    "South Africa": (-30.5595, 22.9375),
    "Nigeria": (9.0820, 8.6753),
    "Kenya": (-0.0236, 37.9062),
    "Morocco": (31.7917, -7.0926),
}


def get_location_coords(country: str, state: Optional[str] = None) -> Tuple[float, float]:
    """Get coordinates for a location."""
    # Try state first (for US)
    if state and state in LOCATION_COORDS:
        return LOCATION_COORDS[state]
    # Try country
    if country in LOCATION_COORDS:
        return LOCATION_COORDS[country]
    # Default to center of world
    return (0.0, 0.0)


def hash_prompt(text: str) -> str:
    """Create a stable hash for prompt lookup.

    Uses first 100 chars + length to handle near-duplicates while
    keeping the hash deterministic for the same prompt.
    """
    key = f"{text[:100]}:{len(text)}"
    return hashlib.md5(key.encode()).hexdigest()[:16]


def main():
    parser = argparse.ArgumentParser(
        description="Preprocess WildChat for GuideLLM with geo-routing"
    )
    parser.add_argument(
        "--num-samples", type=int, default=10000,
        help="Number of samples to extract from WildChat (default: 10000)"
    )
    parser.add_argument(
        "--output-dir", type=str, default=".",
        help="Output directory for generated files (default: current directory)"
    )
    parser.add_argument(
        "--min-prompt-length", type=int, default=10,
        help="Minimum prompt length in characters (default: 10)"
    )
    args = parser.parse_args()

    prompts_file = f"{args.output_dir}/wildchat_guidellm.jsonl"
    lookup_file = f"{args.output_dir}/wildchat_location_lookup.json"

    print(f"Loading WildChat-1M dataset (first {args.num_samples} samples)...")
    ds = datasets.load_dataset(
        "allenai/WildChat-1M",
        split=f"train[:{args.num_samples}]"
    )

    prompts = []
    lookup = {}
    location_stats: Dict[str, int] = {}

    print("Processing conversations...")
    for item in ds:
        # Get conversation
        conv = item.get("conversation", [])
        if not conv:
            continue

        # Find first user message
        prompt = None
        for msg in conv:
            if msg.get("role") == "user":
                prompt = msg.get("content", "")
                break

        if not prompt or len(prompt) < args.min_prompt_length:
            continue

        # Get location data
        country = item.get("country", "United States")
        state = item.get("state")

        # Get coordinates
        lat, lon = get_location_coords(country, state)

        # Compute hash
        h = hash_prompt(prompt)

        # Track location distribution
        loc_key = state if state and state in LOCATION_COORDS else country
        location_stats[loc_key] = location_stats.get(loc_key, 0) + 1

        # Store mapping (hash -> location)
        lookup[h] = {
            "lat": lat,
            "lon": lon,
            "country": country,
            "state": state
        }

        # Store prompt for GuideLLM
        prompts.append({
            "prompt": prompt,
            "hash": h
        })

    # Write prompts file for GuideLLM
    print(f"\nWriting {len(prompts)} prompts to {prompts_file}...")
    with open(prompts_file, "w") as f:
        for p in prompts:
            f.write(json.dumps(p, ensure_ascii=False) + "\n")

    # Write location lookup for geo-proxy
    print(f"Writing {len(lookup)} location mappings to {lookup_file}...")
    with open(lookup_file, "w") as f:
        json.dump(lookup, f, indent=2)

    # Print statistics
    print("\n" + "=" * 60)
    print("PREPROCESSING COMPLETE")
    print("=" * 60)
    print(f"Total prompts extracted: {len(prompts)}")
    print(f"Unique location mappings: {len(lookup)}")
    print(f"\nTop 10 locations by frequency:")
    sorted_locs = sorted(location_stats.items(), key=lambda x: -x[1])[:10]
    for loc, count in sorted_locs:
        pct = 100 * count / len(prompts)
        print(f"  {loc}: {count} ({pct:.1f}%)")

    print(f"\nOutput files:")
    print(f"  - {prompts_file} (for GuideLLM --data)")
    print(f"  - {lookup_file} (for geo-proxy)")


if __name__ == "__main__":
    main()
