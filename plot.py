#!/usr/bin/env python3
"""
Simple script to plot GPU memory usage from CSV file.
Usage: python plot.py gpu-memory.csv
"""

import sys
import csv
import matplotlib.pyplot as plt
from datetime import datetime

if len(sys.argv) < 2:
    print("Usage: python plot.py <gpu-memory.csv>")
    sys.exit(1)

csv_file = sys.argv[1]

timestamps = []
gpu_indices = []
memory_used = []
memory_percent = []

with open(csv_file, 'r') as f:
    reader = csv.DictReader(f)
    for row in reader:
        try:
            ts = datetime.fromisoformat(row['timestamp'].replace('Z', '+00:00'))
            timestamps.append(ts)
            gpu_indices.append(row['gpu_index'])
            memory_used.append(float(row['memory_used_mb']))
            memory_percent.append(float(row['memory_percent']))
        except Exception as e:
            print(f"Error parsing row: {e}")
            continue

if not timestamps:
    print("No data found in CSV file")
    sys.exit(1)

# Group by GPU index
gpu_data = {}
for i, gpu_idx in enumerate(gpu_indices):
    if gpu_idx not in gpu_data:
        gpu_data[gpu_idx] = {'time': [], 'used': [], 'percent': []}
    gpu_data[gpu_idx]['time'].append(timestamps[i])
    gpu_data[gpu_idx]['used'].append(memory_used[i])
    gpu_data[gpu_idx]['percent'].append(memory_percent[i])

# Create plots
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(12, 8))

for gpu_idx, data in gpu_data.items():
    ax1.plot(data['time'], data['used'], label=f'GPU {gpu_idx}', marker='o', markersize=2)
    ax2.plot(data['time'], data['percent'], label=f'GPU {gpu_idx}', marker='o', markersize=2)

ax1.set_xlabel('Time')
ax1.set_ylabel('Memory Used (MB)')
ax1.set_title('GPU Memory Usage Over Time')
ax1.legend()
ax1.grid(True, alpha=0.3)
ax1.tick_params(axis='x', rotation=45)

ax2.set_xlabel('Time')
ax2.set_ylabel('Memory Usage (%)')
ax2.set_title('GPU Memory Usage Percentage')
ax2.legend()
ax2.grid(True, alpha=0.3)
ax2.tick_params(axis='x', rotation=45)

plt.tight_layout()

output_file = csv_file.replace('.csv', '.png')
plt.savefig(output_file, dpi=150)
print(f"Graph saved to: {output_file}")
plt.show()

