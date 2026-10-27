#!/usr/bin/env python3
"""Generate internal/catalog/catalog.json from the models.dev snapshot for OpenCode Go.

Run: python3 scripts/gen_catalog.py [path/to/models.dev/api.json]
If no snapshot path is given, it is downloaded.
"""
import json, sys, urllib.request
from pathlib import Path

PKG = {
    "@ai-sdk/openai-compatible": ("@opencode/ai/providers/openai-compatible", "chat"),
    "@ai-sdk/openai": ("@opencode/ai/providers/openai", "responses"),
    "@ai-sdk/anthropic": ("@opencode/ai/providers/anthropic", "messages"),
    "@ai-sdk/google": ("@opencode/ai/providers/google", "google"),
}

# Monthly included usage (USD) from https://opencode.ai/v2/docs/console/go/
GO = {
    "glm-5.3-flash": 60, "glm-5.3": 15, "glm-5.2": 60,
    "kimi-k3.1-pro": 15, "kimi-k3": 15, "kimi-k2.7-code": 60,
    "longcat-2.0": 60, "step-5-preview": 15,
    "mimo-v2.6-flash": 60, "mimo-v2.6-pro": 15, "mimo-v2.5": 60, "mimo-v2.5-pro": 15,
    "minimax-m3.1-flash": 20, "minimax-m3": 60,
    "muse-spark-1.3-contributor": 60, "muse-spark-1.2-contributor": 60,
    "qwen3.8-max": 15, "qwen3.8-flash": 30, "qwen3.7-plus": 60,
    "deepseek-v4.1-flash": 80, "deepseek-v4-pro": 15,
    "hy4-preview": 30, "hy3": 60, "grok-4.7": 15,
    "gpt-6-luna": 15, "gpt-5.6-luna": 15, "claude-haiku-5-5": 15,
    "gemini-4-argon": 30,
}
GO_PLUS = {
    "glm-5.3-flash": 180, "glm-5.3": 120, "glm-5.2": 180,
    "kimi-k3.1-pro": 60, "kimi-k3": 60, "kimi-k2.7-code": 180,
    "longcat-2.0": 240, "step-5-preview": 60,
    "mimo-v2.6-flash": 120, "mimo-v2.6-pro": 60, "mimo-v2.5": 120, "mimo-v2.5-pro": 60,
    "minimax-m3.1-flash": 80, "minimax-m3": 180,
    "muse-spark-1.3-contributor": 120, "muse-spark-1.2-contributor": 120,
    "qwen3.8-max": 60, "qwen3.8-flash": 90, "qwen3.7-plus": 180,
    "deepseek-v4.1-flash": 160, "deepseek-v4-pro": 60,
    "hy4-preview": 120, "hy3": 240, "grok-4.7": 60,
    "gpt-6-luna": 60, "gpt-5.6-luna": 60, "claude-haiku-5-5": 60,
    "gemini-4-argon": 120,
}

def main():
    src = sys.argv[1] if len(sys.argv) > 1 else None
    if src:
        raw = json.load(open(src))
    else:
        raw = json.load(urllib.request.urlopen("https://models.dev/api.json"))
    provider = raw["opencode-go"]
    out = []
    for mid, m in provider["models"].items():
        npm = m.get("provider", {}).get("npm", "@ai-sdk/openai-compatible")
        package, api = PKG.get(npm, PKG["@ai-sdk/openai-compatible"])
        cost = m.get("cost", {}) or {}
        limit = m.get("limit", {}) or {}
        out.append({
            "id": mid,
            "name": m.get("name", mid),
            "family": m.get("family", ""),
            "package": package,
            "api_family": api,
            "reasoning": bool(m.get("reasoning")),
            "tool_call": bool(m.get("tool_call")),
            "context": int(limit.get("context", 0) or 0),
            "input_limit": int(limit.get("input", 0) or 0),
            "output_limit": int(limit.get("output", 0) or 0),
            "cost": {
                "input": float(cost.get("input", 0) or 0),
                "output": float(cost.get("output", 0) or 0),
                "cache_read": float(cost.get("cache_read", 0) or 0),
                "cache_write": float(cost.get("cache_write", 0) or 0),
            },
            "deprecated": m.get("status") == "deprecated",
            "go_monthly": GO.get(mid),
            "go_plus_monthly": GO_PLUS.get(mid),
        })
    out.sort(key=lambda x: (x["deprecated"], x["name"].lower()))
    dest = Path(__file__).resolve().parent.parent / "internal/catalog/catalog.json"
    dest.write_text(json.dumps(out, indent=2) + "\n")
    print(f"wrote {len(out)} models to {dest}")

if __name__ == "__main__":
    main()
