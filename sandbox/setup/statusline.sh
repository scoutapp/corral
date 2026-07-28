#!/bin/bash
input=$(cat)

# ANSI colors
GREEN='\033[32m'
YELLOW='\033[33m'
RED='\033[31m'
RESET='\033[0m'

# --- Field extraction ---
MODEL=$(echo "$input" | jq -r '.model.display_name')
DIR=$(echo "$input" | jq -r '.workspace.current_dir')
TOTAL_TOKENS=$(echo "$input" | jq -r '(.context_window.total_input_tokens // 0) + (.context_window.total_output_tokens // 0)')
CW_PCT=$(echo "$input" | jq -r '.context_window.used_percentage // 0 | floor')
FIVE_H_PCT=$(echo "$input" | jq -r '.rate_limits.five_hour.used_percentage // 0 | floor')
SEVEN_D_PCT=$(echo "$input" | jq -r '.rate_limits.seven_day.used_percentage // 0 | floor')

# --- Git status ---
GIT_PART=""
if git -C "$DIR" rev-parse --git-dir > /dev/null 2>&1; then
    BRANCH=$(git -C "$DIR" branch --show-current 2>/dev/null)
    STAGED=$(git -C "$DIR" diff --cached --numstat 2>/dev/null | wc -l | tr -d ' ')
    MODIFIED=$(git -C "$DIR" diff --numstat 2>/dev/null | wc -l | tr -d ' ')

    GIT_PART="🌿 ${BRANCH}"
    [ "$STAGED" -gt 0 ]   && GIT_PART="${GIT_PART} ${GREEN}+${STAGED}${RESET}"
    [ "$MODIFIED" -gt 0 ] && GIT_PART="${GIT_PART}${YELLOW}~${MODIFIED}${RESET}"
    GIT_PART="${GIT_PART} | "
fi

# --- Progress bar builder (color-coded by threshold) ---
make_bar() {
    local pct=$1 width=8
    local filled=$(( pct * width / 100 ))
    local empty=$(( width - filled ))
    local color

    if   [ "$pct" -ge 80 ]; then color="$RED"
    elif [ "$pct" -ge 60 ]; then color="$YELLOW"
    else color="$GREEN"
    fi

    local bar=""
    [ "$filled" -gt 0 ] && { printf -v FILL "%${filled}s"; bar="${FILL// /▓}"; }
    [ "$empty"  -gt 0 ] && { printf -v PAD  "%${empty}s";  bar="${bar}${PAD// /░}"; }

    printf "%b" "${color}${bar}${RESET} ${pct}%"
}

# --- Line 1: [Model] | git status | total tokens ---
echo -e "[$MODEL] | ${GIT_PART}${TOTAL_TOKENS} tokens"

# --- Line 2: Usage bars ---
echo -e "CW: $(make_bar "$CW_PCT") | 5H: $(make_bar "$FIVE_H_PCT") | 7D: $(make_bar "$SEVEN_D_PCT")"
