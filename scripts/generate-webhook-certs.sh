#!/bin/bash

# Webhook Certificate Generation Script for SBD Operator
# Supports both self-signed certificates (for testing) and Let's Encrypt certificates (for production)

set -e

# Default configuration
USE_LETSENCRYPT="${USE_LETSENCRYPT:-false}"
WEBHOOK_DOMAIN="${WEBHOOK_DOMAIN:-sbd-webhook.default.svc}"
NAMESPACE="${NAMESPACE:-sbd-operator-system}"
CERT_DIR="${CERT_DIR:-/tmp/k8s-webhook-server/serving-certs}"
LETSENCRYPT_EMAIL="${LETSENCRYPT_EMAIL:-}"
LETSENCRYPT_STAGING="${LETSENCRYPT_STAGING:-true}"

# Certificate file names
CERT_NAME="tls.crt"
KEY_NAME="tls.key"
CA_NAME="ca.crt"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to generate self-signed certificates
generate_self_signed_certs() {
    log_info "Generating self-signed webhook certificates..."
    
    # Create certificate directory
    mkdir -p "$CERT_DIR"
    
    # Generate CA private key
    openssl genrsa -out "$CERT_DIR/ca.key" 2048
    
    # Generate CA certificate
    openssl req -new -x509 -days 365 -key "$CERT_DIR/ca.key" \
        -subj "/C=US/ST=CA/L=San Francisco/O=SBD Operator/CN=SBD Webhook CA" \
        -out "$CERT_DIR/$CA_NAME"
    
    # Generate webhook private key
    openssl genrsa -out "$CERT_DIR/$KEY_NAME" 2048
    
    # Create certificate signing request config
    cat > "$CERT_DIR/webhook.conf" <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name

[req_distinguished_name]

[v3_req]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = sbd-operator-webhook-service
DNS.2 = sbd-operator-webhook-service.${NAMESPACE}
DNS.3 = sbd-operator-webhook-service.${NAMESPACE}.svc
DNS.4 = sbd-operator-webhook-service.${NAMESPACE}.svc.cluster.local
DNS.5 = ${WEBHOOK_DOMAIN}
EOF

    # Generate certificate signing request
    openssl req -new -key "$CERT_DIR/$KEY_NAME" \
        -subj "/C=US/ST=CA/L=San Francisco/O=SBD Operator/CN=sbd-operator-webhook-service.${NAMESPACE}.svc" \
        -out "$CERT_DIR/webhook.csr" \
        -config "$CERT_DIR/webhook.conf"
    
    # Generate webhook certificate signed by CA
    openssl x509 -req -in "$CERT_DIR/webhook.csr" \
        -CA "$CERT_DIR/$CA_NAME" -CAkey "$CERT_DIR/ca.key" \
        -CAcreateserial -out "$CERT_DIR/$CERT_NAME" \
        -days 365 -extensions v3_req \
        -extfile "$CERT_DIR/webhook.conf"
    
    # Clean up temporary files
    rm -f "$CERT_DIR/webhook.csr" "$CERT_DIR/webhook.conf" "$CERT_DIR/ca.key" "$CERT_DIR/ca.srl"
    
    # Set appropriate permissions
    chmod 600 "$CERT_DIR/$KEY_NAME"
    chmod 644 "$CERT_DIR/$CERT_NAME" "$CERT_DIR/$CA_NAME"
    
    log_info "Self-signed certificates generated successfully in $CERT_DIR"
}

# Function to generate Let's Encrypt certificates
generate_letsencrypt_certs() {
    log_info "Generating Let's Encrypt certificates..."
    
    if [[ -z "$LETSENCRYPT_EMAIL" ]]; then
        log_error "LETSENCRYPT_EMAIL must be set for Let's Encrypt certificate generation"
        exit 1
    fi
    
    if [[ -z "$WEBHOOK_DOMAIN" ]]; then
        log_error "WEBHOOK_DOMAIN must be set for Let's Encrypt certificate generation"
        exit 1
    fi
    
    # Check if certbot is installed
    if ! command -v certbot &> /dev/null; then
        log_error "certbot is required for Let's Encrypt certificates but is not installed"
        log_info "Install certbot: pip install certbot"
        exit 1
    fi
    
    # Create certificate directory
    mkdir -p "$CERT_DIR"
    
    # Set staging flag
    STAGING_FLAG=""
    if [[ "$LETSENCRYPT_STAGING" == "true" ]]; then
        STAGING_FLAG="--staging"
        log_warn "Using Let's Encrypt staging environment (certificates will not be trusted)"
    fi
    
    # Generate certificates using certbot
    log_info "Requesting certificates for domain: $WEBHOOK_DOMAIN"
    certbot certonly \
        --standalone \
        --non-interactive \
        --agree-tos \
        --email "$LETSENCRYPT_EMAIL" \
        --domains "$WEBHOOK_DOMAIN" \
        $STAGING_FLAG \
        --cert-path "$CERT_DIR/$CERT_NAME" \
        --key-path "$CERT_DIR/$KEY_NAME" \
        --fullchain-path "$CERT_DIR/$CA_NAME"
    
    log_info "Let's Encrypt certificates generated successfully in $CERT_DIR"
}

# Function to validate certificates
validate_certificates() {
    log_info "Validating generated certificates..."
    
    if [[ ! -f "$CERT_DIR/$CERT_NAME" || ! -f "$CERT_DIR/$KEY_NAME" ]]; then
        log_error "Certificate files not found in $CERT_DIR"
        return 1
    fi
    
    # Check certificate validity
    if ! openssl x509 -in "$CERT_DIR/$CERT_NAME" -noout -text &>/dev/null; then
        log_error "Generated certificate is invalid"
        return 1
    fi
    
    # Check key validity
    if ! openssl rsa -in "$CERT_DIR/$KEY_NAME" -check -noout &>/dev/null; then
        log_error "Generated private key is invalid"
        return 1
    fi
    
    # Check if certificate and key match
    cert_modulus=$(openssl x509 -noout -modulus -in "$CERT_DIR/$CERT_NAME" | openssl md5)
    key_modulus=$(openssl rsa -noout -modulus -in "$CERT_DIR/$KEY_NAME" | openssl md5)
    
    if [[ "$cert_modulus" != "$key_modulus" ]]; then
        log_error "Certificate and private key do not match"
        return 1
    fi
    
    log_info "Certificate validation successful"
    
    # Display certificate information
    log_info "Certificate details:"
    openssl x509 -in "$CERT_DIR/$CERT_NAME" -noout -subject -issuer -dates
}

# Main function
main() {
    log_info "SBD Operator Webhook Certificate Generator"
    log_info "=========================================="
    log_info "Configuration:"
    log_info "  Use Let's Encrypt: $USE_LETSENCRYPT"
    log_info "  Webhook Domain: $WEBHOOK_DOMAIN"
    log_info "  Namespace: $NAMESPACE"
    log_info "  Certificate Directory: $CERT_DIR"
    
    # Check if openssl is available
    if ! command -v openssl &> /dev/null; then
        log_error "openssl is required but not installed"
        exit 1
    fi
    
    # Generate certificates based on configuration
    if [[ "$USE_LETSENCRYPT" == "true" ]]; then
        generate_letsencrypt_certs
    else
        generate_self_signed_certs
    fi
    
    # Validate generated certificates
    validate_certificates
    
    log_info "Webhook certificate generation completed successfully!"
    log_info "Certificates are available in: $CERT_DIR"
}

# Run main function
main "$@" 