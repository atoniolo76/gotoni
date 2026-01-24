from datasets import load_dataset
import pandas as pd

# Load WildChat
ds = load_dataset("allenai/WildChat", split="train", streaming=True).take(100)

flat_data = []
for entry in ds:
    # Use the first user message as the 'article'
    prompt = entry['conversation'][0]['content']
    # Use the first assistant response length as the target 'output tokens'
    output_len = len(entry['conversation'][1]['content'].split()) if len(entry['conversation']) > 1 else 100
    
    flat_data.append({"article": prompt, "summary_tokens": output_len})

pd.DataFrame(flat_data).to_json("wildchat_flat.jsonl", orient="records", lines=True)