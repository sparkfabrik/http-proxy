FROM grafana/grafana:12.4.0

# Copy provisioning configurations
COPY provisioning/datasources /etc/grafana/provisioning/datasources
COPY provisioning/dashboards /etc/grafana/provisioning/dashboards

# Copy dashboard JSON files
COPY dashboards /var/lib/grafana/dashboards

# Expose port 3000 (Grafana's default port)
EXPOSE 3000
