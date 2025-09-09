FROM traefik:v3.5.1

# Accept git version as build argument
ARG GIT_VERSION=unknown

# Copy static configuration and entrypoint script
COPY traefik.yml /etc/traefik/traefik.yml
COPY entrypoint.sh /entrypoint.sh

# Make entrypoint executable
RUN chmod +x /entrypoint.sh

# Create directories for dynamic configuration and user certificates
RUN mkdir -p /traefik/dynamic /traefik/certs

# Save git version information to a file
RUN echo "${GIT_VERSION}" > /.version

# Use custom entrypoint that processes certificates and starts Traefik
ENTRYPOINT ["/entrypoint.sh", "--configfile=/etc/traefik/traefik.yml"]

# Expose ports
EXPOSE 80 443 8080 8082

# Use the default traefik entrypoint
CMD ["traefik"]
