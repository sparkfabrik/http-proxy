#!/usr/bin/env bash

#
# Spark HTTP Proxy Installer
#
# This script installs the Spark HTTP Proxy by:
# 1. Cloning the repository to ~/.local/spark/http-proxy/src
# 2. Creating a symlink to the binary in /usr/local/bin
# 3. Setting up shell completion (optional)
#
# Usage:
#   bash <(curl -fsSL https://raw.githubusercontent.com/sparkfabrik/http-proxy/main/install.sh)
#
# Environment Variables:
#   INSTALL_DIR       - Installation directory (default: ~/.local/spark/http-proxy)
#   BIN_DIR          - Binary directory (default: /usr/local/bin)
#   SKIP_CONFIRM     - Skip confirmation prompts (default: false)
#   INSTALL_COMPLETION - Install shell completion (default: true)
#

set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
REPO_URL="https://github.com/sparkfabrik/http-proxy.git"
REPO_BRANCH="feature/98_migrate_to_traefik"
DEFAULT_INSTALL_DIR="${HOME}/.local/spark/http-proxy"
DEFAULT_BIN_DIR="/usr/local/bin"
BINARY_NAME="spark-http-proxy"

# User configurable variables
INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
BIN_DIR="${BIN_DIR:-$DEFAULT_BIN_DIR}"
SKIP_CONFIRM="${SKIP_CONFIRM:-false}"
INSTALL_COMPLETION="${INSTALL_COMPLETION:-true}"

SRC_DIR="${INSTALL_DIR}/src"
BINARY_SRC="${SRC_DIR}/bin/${BINARY_NAME}"
BINARY_TARGET="${BIN_DIR}/${BINARY_NAME}"

# Logging functions
log_info() { echo -e "${BLUE}ℹ️  $1${NC}"; }
log_success() { echo -e "${GREEN}✅ $1${NC}"; }
log_error() { echo -e "${RED}❌ $1${NC}"; }
log_warning() { echo -e "${YELLOW}⚠️  $1${NC}"; }

# Error handling
cleanup() {
    local exit_code=$?
    if [[ $exit_code -ne 0 ]]; then
        log_error "Installation failed with exit code $exit_code"
        log_info "You can try running the installation again or install manually:"
        log_info "  git clone --branch ${REPO_BRANCH} ${REPO_URL} ${SRC_DIR}"
        log_info "  sudo ln -sf ${BINARY_SRC} ${BINARY_TARGET}"
    fi
}
trap cleanup EXIT

# Check if running with sudo (not recommended)
check_sudo() {
    if [[ $EUID -eq 0 ]]; then
        log_warning "Running as root is not recommended"
        log_info "This script will prompt for sudo access only when needed"
        if [[ "${SKIP_CONFIRM}" != "true" ]]; then
            read -p "Continue anyway? (y/N): " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                log_info "Installation cancelled"
                exit 1
            fi
        fi
    fi
}

# Check prerequisites
check_prerequisites() {
    local missing=()

    command -v git >/dev/null 2>&1 || missing+=("git")

    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "Missing required dependencies: ${missing[*]}"
        log_info "Please install the missing dependencies and try again"
        exit 1
    fi
}

# Check if binary directory is writable or if we can use sudo
check_bin_dir_access() {
    SUDO_REQUIRED_MSG=""
    if [[ -w "$BIN_DIR" ]]; then
        SUDO_CMD=""
    elif command -v sudo >/dev/null 2>&1; then
        SUDO_CMD="sudo"
        SUDO_REQUIRED_MSG=" (sudo access is required)"
    else
        log_error "Cannot write to $BIN_DIR and sudo is not available"
        log_info "Please run as a user with write access to $BIN_DIR or install sudo"
        exit 1
    fi
}

# Confirm installation
confirm_installation() {
    if [[ "${SKIP_CONFIRM}" == "true" ]]; then
        return 0
    fi

    echo
    log_info "Spark HTTP Proxy Installation Summary:"
    echo "  Repository: ${REPO_URL}"
    echo "  Branch: ${REPO_BRANCH}"
    echo "  Install to: ${SRC_DIR}"
    echo "  Binary link: ${BINARY_TARGET}${SUDO_REQUIRED_MSG}"
    echo "  Shell completion: ${INSTALL_COMPLETION}"
    echo

    read -p "Continue with installation? (Y/n): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Nn]$ ]]; then
        log_info "Installation cancelled"
        exit 0
    fi
}

# Check if already installed
check_existing_installation() {
    if [[ -d "$SRC_DIR" ]]; then
        log_error "Installation directory already exists: $SRC_DIR"
        log_info "Please remove it manually before running the installer again."
        trap - EXIT
        exit 1
    fi

    if [[ -e "$BINARY_TARGET" ]]; then
        log_error "Binary target already exists: $BINARY_TARGET"
        log_info "Please remove it manually before running the installer again."
        trap - EXIT
        exit 1
    fi
}

# Install the software
install_proxy() {
    log_info "Creating installation directory..."
    mkdir -p "$INSTALL_DIR"

    log_info "Cloning repository..."
    git clone --quiet --branch "$REPO_BRANCH" "$REPO_URL" "$SRC_DIR"

    log_info "Verifying installation..."
    if [[ ! -f "$BINARY_SRC" ]]; then
        log_error "Binary not found at expected location: $BINARY_SRC"
        exit 1
    fi

    # Make binary executable
    chmod +x "$BINARY_SRC"

    log_info "Creating symlink..."
    $SUDO_CMD mkdir -p "$BIN_DIR"
    $SUDO_CMD ln -sf "$BINARY_SRC" "$BINARY_TARGET"

    # Verify symlink
    if [[ ! -x "$BINARY_TARGET" ]]; then
        log_error "Failed to create working symlink at $BINARY_TARGET"
        exit 1
    fi
}

# Test installation
test_installation() {
    log_info "Testing installation..."

    if ! command -v "$BINARY_NAME" >/dev/null 2>&1; then
        log_error "Binary not found in PATH"
        log_info "You may need to add $BIN_DIR to your PATH"
        exit 1
    fi

    # Simple test to verify the binary is executable and accessible
    log_info "Verifying binary is executable..."
    if [[ ! -x "$BINARY_TARGET" ]]; then
        log_error "Binary is not executable"
        exit 1
    fi

    log_success "Installation test passed"

    # Install shell completion after successful test
    if [[ "${INSTALL_COMPLETION}" == "true" ]]; then
        log_info "Installing shell completion..."
        if "$BINARY_TARGET" install-completion 2>/dev/null; then
            log_success "Shell completion installed"
            COMPLETION_INSTALLED=true
        else
            log_warning "Failed to install shell completion (non-fatal)"
            COMPLETION_INSTALLED=false
        fi
    else
        COMPLETION_INSTALLED=false
    fi
}

# Show next steps
show_next_steps() {
    echo
    log_success "🎉 Spark HTTP Proxy installed successfully!"
    echo
    log_info "Quick start commands:"
    echo "  $BINARY_NAME help              # Show help"
    echo "  $BINARY_NAME start             # Start HTTP proxy"
    echo "  $BINARY_NAME configure-dns     # Configure system DNS"
    echo "  $BINARY_NAME status            # Check status"
    echo
    log_info "For more information, visit:"
    echo "  📖 Documentation: https://github.com/sparkfabrik/http-proxy#readme"
    echo "  💾 Installation location: $SRC_DIR"
    echo "  🔗 Binary location: $BINARY_TARGET"
    echo

    if [[ "${COMPLETION_INSTALLED}" == "true" ]]; then
        log_info "💡 Shell completion installed - restart your terminal for tab completion"
    else
        log_info "💡 To enable tab completion: $BINARY_NAME install-completion"
    fi
}

# Main installation flow
main() {
    echo "🚀 Starting Spark HTTP Proxy installation..."
    check_sudo
    check_prerequisites
    check_bin_dir_access
    confirm_installation
    check_existing_installation
    install_proxy
    test_installation
    show_next_steps
}

# Run main function
main "$@"
