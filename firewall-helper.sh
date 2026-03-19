#!/bin/bash
# Firewall Helper - Interactive permission system for blocked connections
# This script monitors firewall logs and prompts the user to allow blocked domains

set -euo pipefail

FIREWALL_CONFIG_DIR="/home/claude/.firewall"
ALLOWED_DOMAINS_FILE="$FIREWALL_CONFIG_DIR/allowed-domains.txt"
BLOCKED_CACHE_FILE="$FIREWALL_CONFIG_DIR/blocked-cache.txt"
INTERACTIVE_MODE_FILE="$FIREWALL_CONFIG_DIR/interactive-mode"

# Create config directory if it doesn't exist
mkdir -p "$FIREWALL_CONFIG_DIR"
touch "$BLOCKED_CACHE_FILE"

# Function to extract IP from log line
extract_ip_from_log() {
    local log_line="$1"
    # Extract DST=x.x.x.x from iptables log
    echo "$log_line" | grep -oP 'DST=\K[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' || echo ""
}

# Function to reverse DNS lookup
get_hostname_from_ip() {
    local ip="$1"
    # Try reverse DNS lookup
    hostname=$(dig +short -x "$ip" 2>/dev/null | sed 's/\.$//' || echo "")

    if [ -z "$hostname" ]; then
        echo "$ip"
    else
        echo "$hostname"
    fi
}

# Function to check if IP/domain was already handled
is_cached() {
    local identifier="$1"
    grep -qF "$identifier" "$BLOCKED_CACHE_FILE" 2>/dev/null
}

# Function to add to cache
add_to_cache() {
    local identifier="$1"
    echo "$identifier" >> "$BLOCKED_CACHE_FILE"
}

# Function to add domain to allowed list
add_domain_to_firewall() {
    local domain="$1"

    # Check if domain already exists
    if grep -qF "$domain" "$ALLOWED_DOMAINS_FILE" 2>/dev/null; then
        echo "Domain $domain is already in the allowed list"
        return 0
    fi

    # Add domain to file
    echo "$domain" >> "$ALLOWED_DOMAINS_FILE"
    echo "Added $domain to allowed domains"

    # Reload firewall
    echo "Reloading firewall configuration..."
    sudo /usr/local/bin/init-firewall.sh > /dev/null 2>&1
    echo "Firewall reloaded successfully"
}

# Function to prompt user for permission
prompt_user() {
    local ip="$1"
    local hostname="$2"

    echo ""
    echo "=========================================="
    echo "FIREWALL BLOCKED CONNECTION"
    echo "=========================================="
    echo "IP Address: $ip"
    echo "Hostname:   $hostname"
    echo ""
    echo "Do you want to allow this domain permanently?"
    echo "  [y] Yes - Add to firewall and allow"
    echo "  [n] No  - Keep blocking"
    echo "  [o] Once - Allow this session only (not implemented yet)"
    echo "  [q] Quit monitoring"
    echo ""
    read -r -p "Choice [y/n/o/q]: " choice

    case "$choice" in
        y|Y)
            add_domain_to_firewall "$hostname"
            add_to_cache "$ip"
            return 0
            ;;
        n|N)
            echo "Domain will remain blocked"
            add_to_cache "$ip"
            return 0
            ;;
        o|O)
            echo "Session-only allow not yet implemented"
            add_to_cache "$ip"
            return 0
            ;;
        q|Q)
            echo "Exiting firewall monitor..."
            return 1
            ;;
        *)
            echo "Invalid choice, keeping domain blocked"
            add_to_cache "$ip"
            return 0
            ;;
    esac
}

# Main monitoring function
monitor_firewall() {
    echo "Firewall Helper - Interactive Permission System"
    echo "Monitoring blocked connections..."
    echo "Press Ctrl+C to stop monitoring"
    echo ""

    # Check if interactive mode is enabled
    INTERACTIVE_MODE=$(cat "$INTERACTIVE_MODE_FILE" 2>/dev/null || echo "enabled")
    if [ "$INTERACTIVE_MODE" != "enabled" ]; then
        echo "Interactive mode is disabled"
        echo "To enable: echo 'enabled' > $INTERACTIVE_MODE_FILE"
        exit 1
    fi

    # Follow kernel log for firewall blocks
    sudo dmesg -w | grep --line-buffered "FIREWALL_BLOCKED" | while read -r line; do
        # Extract IP address
        ip=$(extract_ip_from_log "$line")

        if [ -z "$ip" ]; then
            continue
        fi

        # Skip if already handled
        if is_cached "$ip"; then
            continue
        fi

        # Get hostname
        hostname=$(get_hostname_from_ip "$ip")

        # Prompt user
        if ! prompt_user "$ip" "$hostname"; then
            break
        fi
    done
}

# Function to list allowed domains
list_domains() {
    echo "Currently allowed domains:"
    echo "=========================================="
    cat "$ALLOWED_DOMAINS_FILE" 2>/dev/null || echo "No domains configured"
}

# Function to remove a domain
remove_domain() {
    local domain="$1"

    if ! grep -qF "$domain" "$ALLOWED_DOMAINS_FILE" 2>/dev/null; then
        echo "Domain $domain not found in allowed list"
        return 1
    fi

    # Remove domain from file
    sed -i.bak "/^$(echo "$domain" | sed 's/[.[\*^$()+?{|]/\\&/g')$/d" "$ALLOWED_DOMAINS_FILE"
    rm -f "${ALLOWED_DOMAINS_FILE}.bak"

    echo "Removed $domain from allowed domains"

    # Reload firewall
    echo "Reloading firewall configuration..."
    sudo /usr/local/bin/init-firewall.sh > /dev/null 2>&1
    echo "Firewall reloaded successfully"
}

# Function to clear blocked cache
clear_cache() {
    > "$BLOCKED_CACHE_FILE"
    echo "Blocked cache cleared"
}

# CLI interface
case "${1:-monitor}" in
    monitor)
        monitor_firewall
        ;;
    list)
        list_domains
        ;;
    add)
        if [ -z "${2:-}" ]; then
            echo "Usage: $0 add <domain>"
            exit 1
        fi
        add_domain_to_firewall "$2"
        ;;
    remove)
        if [ -z "${2:-}" ]; then
            echo "Usage: $0 remove <domain>"
            exit 1
        fi
        remove_domain "$2"
        ;;
    clear-cache)
        clear_cache
        ;;
    *)
        echo "Firewall Helper - Interactive Permission System"
        echo ""
        echo "Usage: $0 [command] [arguments]"
        echo ""
        echo "Commands:"
        echo "  monitor              Monitor and prompt for blocked connections (default)"
        echo "  list                 List all allowed domains"
        echo "  add <domain>         Add a domain to the allowed list"
        echo "  remove <domain>      Remove a domain from the allowed list"
        echo "  clear-cache          Clear the blocked connections cache"
        echo ""
        echo "Examples:"
        echo "  $0 monitor"
        echo "  $0 add example.com"
        echo "  $0 list"
        exit 1
        ;;
esac
