#!/usr/bin/env python3
"""
Script to extract and concatenate model output chunks from log files.

Usage:
    python extract_model_output.py <log_file>

The script will output the concatenated model response to stdout.
"""

import json
import sys
import re
from pathlib import Path


def extract_model_output(log_file_path):
    """
    Extract concatenated model output from a log file.
    
    Args:
        log_file_path: Path to the log file
        
    Returns:
        str: Concatenated model output
    """
    model_output = []
    
    with open(log_file_path, 'r', encoding='utf-8') as f:
        for line in f:
            line = line.strip()
            
            # Look for lines that start with "data: " and contain JSON
            if line.startswith('data: '):
                try:
                    # Extract the JSON part after "data: "
                    json_str = line[6:]  # Remove "data: " prefix
                    
                    # Parse the JSON
                    data = json.loads(json_str)
                    
                    # Extract content from choices[0].delta.content
                    if 'choices' in data and len(data['choices']) > 0:
                        choice = data['choices'][0]
                        if 'delta' in choice and 'content' in choice['delta']:
                            content = choice['delta']['content']
                            if content:  # Only add non-empty content
                                model_output.append(content)
                                
                except (json.JSONDecodeError, KeyError, IndexError) as e:
                    # Skip malformed JSON or missing fields
                    continue
    
    return ''.join(model_output)


def main():
    if len(sys.argv) != 2:
        print("Usage: python extract_model_output.py <log_file>", file=sys.stderr)
        sys.exit(1)
    
    log_file = Path(sys.argv[1])
    
    if not log_file.exists():
        print(f"Error: File '{log_file}' does not exist.", file=sys.stderr)
        sys.exit(1)
    
    try:
        output = extract_model_output(log_file)
        print(output)
    except Exception as e:
        print(f"Error processing file: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()




