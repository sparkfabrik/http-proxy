FROM prom/prometheus:v3.5.0

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
