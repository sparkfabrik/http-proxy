FROM traefik:v3.4

# Copy static configuration and entrypoint script
COPY traefik.yml /etc/traefik/traefik.yml
COPY entrypoint.sh /entrypoint.sh

# Make entrypoint executable
RUN chmod +x /entrypoint.sh

# Create directories for dynamic configuration and user certificates
RUN mkdir -p /traefik/dynamic /traefik/certs

# Use custom entrypoint that processes certificates and starts Traefik
ENTRYPOINT ["/entrypoint.sh", "--configfile=/etc/traefik/traefik.yml"]

# Expose ports
EXPOSE 80 443 8080 8082

# Use the default traefik entrypoint
CMD ["traefik"]
