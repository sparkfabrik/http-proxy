FROM prom/prometheus:v3.10.0@sha256:4a61322ac1103a0e3aea2a61ef1718422a48fa046441f299d71e660a3bc71ae9

# Copy the Prometheus configuration
COPY prometheus.yml /etc/prometheus/prometheus.yml

# Expose port 9090 (Prometheus default port)
EXPOSE 9090

# Use the default Prometheus entrypoint with our config
CMD ["--config.file=/etc/prometheus/prometheus.yml", \
     "--storage.tsdb.path=/prometheus", \
     "--web.console.libraries=/etc/prometheus/console_libraries", \
     "--web.console.templates=/etc/prometheus/consoles", \
     "--web.enable-lifecycle"]
