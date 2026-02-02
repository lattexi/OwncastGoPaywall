# Deployment Guide

This guide covers deploying the Stream Paywall system in production.

## Prerequisites

- Docker and Docker Compose installed
- A domain name with SSL certificate (for HTTPS)
- Paytrail merchant account
- Server with at least 2GB RAM

## Quick Deploy with Docker Compose

### 1. Clone and Configure

```bash
git clone https://github.com/youruser/stream-paywall.git
cd stream-paywall

# Copy and edit environment file
cp .env.example .env
nano .env
```

### 2. Required Environment Variables

```bash
# CRITICAL: Change these in production!
SIGNING_SECRET=your-secure-random-string-at-least-32-chars
ADMIN_API_KEY=your-secure-admin-api-key
POSTGRES_PASSWORD=your-secure-database-password

# Paytrail (get from merchant panel)
PAYTRAIL_MERCHANT_ID=your-merchant-id
PAYTRAIL_SECRET_KEY=your-secret-key

# Your public domain
BASE_URL=https://stream.yourdomain.com
```

### 3. Start Services

```bash
# Build and start all services
docker compose up -d

# Check status
docker compose ps

# View logs
docker compose logs -f paywall
```

SRS config files are stored in `/var/lib/stream-paywall/srs-configs` (bind-mounted so the Docker daemon can mount them into SRS containers). On first run, create it if your environment does not create it automatically: `sudo mkdir -p /var/lib/stream-paywall/srs-configs`.

## Reverse Proxy Setup (Nginx)

The paywall server should be behind a reverse proxy for HTTPS termination.

### Nginx Configuration

```nginx
server {
    listen 80;
    server_name stream.yourdomain.com;
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name stream.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/stream.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/stream.yourdomain.com/privkey.pem;

    # SSL configuration
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256;
    ssl_prefer_server_ciphers off;

    # Security headers
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "no-referrer-when-downgrade" always;

    # Proxy to paywall server
    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;

        # Timeouts for streaming
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }

    # HLS segments - allow caching
    location /stream/ {
        proxy_pass http://127.0.0.1:3000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Buffer settings for video
        proxy_buffering on;
        proxy_buffer_size 128k;
        proxy_buffers 4 256k;
        proxy_busy_buffers_size 256k;
    }
}
```

## SSL Certificate (Let's Encrypt)

```bash
# Install certbot
apt install certbot python3-certbot-nginx

# Obtain certificate
certbot --nginx -d stream.yourdomain.com

# Auto-renewal is configured automatically
```

## Scaling Considerations

### Horizontal Scaling

The paywall server is stateless and can be scaled horizontally:

1. Run multiple paywall containers
2. Use a load balancer (nginx, HAProxy, or cloud LB)
3. Ensure all instances connect to the same Redis and PostgreSQL

```yaml
# docker-compose.override.yml for scaling
services:
  paywall:
    deploy:
      replicas: 3
```

### Database Scaling

For high traffic:
- Use PostgreSQL connection pooling (PgBouncer)
- Consider read replicas for read-heavy operations
- Enable PostgreSQL performance monitoring

### Redis Scaling

For high traffic:
- Use Redis Sentinel for high availability
- Consider Redis Cluster for horizontal scaling

## Monitoring

### Health Check

The server exposes a health endpoint:

```bash
curl http://localhost:3000/health
# {"status":"ok"}
```

### Prometheus Metrics (Future)

Add prometheus metrics endpoint for monitoring.

### Logging

Logs are in JSON format in production:

```bash
# View structured logs
docker compose logs paywall | jq .
```

## Backup Strategy

### PostgreSQL Backup

```bash
# Manual backup
docker exec paywall-postgres pg_dump -U paywall paywall > backup.sql

# Automated daily backup (cron)
0 2 * * * docker exec paywall-postgres pg_dump -U paywall paywall > /backups/paywall-$(date +\%Y\%m\%d).sql
```

### Redis Backup

Redis is configured with AOF persistence. Backup the data volume:

```bash
docker cp paywall-redis:/data /backups/redis-$(date +%Y%m%d)
```

## Troubleshooting

### Common Issues

**Payment callbacks failing**
- Ensure `BASE_URL` is publicly accessible
- Check Paytrail merchant panel for callback status
- Verify HTTPS certificate is valid

**Streams not playing**
- Check Owncast is running: `docker compose logs owncast`
- Verify Owncast URL in stream configuration
- Check signed URL expiry times

**High memory usage**
- Monitor Redis memory: `redis-cli INFO memory`
- Check PostgreSQL connections: `SELECT count(*) FROM pg_stat_activity`

### Debug Mode

To enable debug logging:

```bash
ENV=development docker compose up paywall
```

## Security Checklist

- [ ] Changed all default passwords
- [ ] Set strong `SIGNING_SECRET` (32+ random chars)
- [ ] Set strong `ADMIN_API_KEY`
- [ ] HTTPS enabled with valid certificate
- [ ] Firewall configured (only ports 80/443 exposed)
- [ ] Regular backups configured
- [ ] Log monitoring enabled
- [ ] Rate limiting enabled on reverse proxy
