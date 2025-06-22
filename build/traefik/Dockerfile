FROM traefik:v3.4

# Copy static configuration
COPY traefik.yml /etc/traefik/traefik.yml

# Create dynamic configurations directory
RUN mkdir -p /traefik/dynamic

# Expose ports
EXPOSE 80 8080

# Use the default traefik entrypoint
CMD ["traefik"]
