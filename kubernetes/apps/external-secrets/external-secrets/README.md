# external-secrets

## 1Password Connect on my HC NAS

### onepassword-connect
generate the base64 encoded value for OP_SESSION using below
https://developer.1password.com/docs/connect/aws-ecs-fargate

cat 1password-credentials.json | base64 | tr '/+' '_-' | tr -d '=' | tr -d '\n'

```yaml
services:
  onepassword-connect-api:
    container_name: onepassword-connect-api
    environment:
      OP_HTTPS_PORT: "443"
      OP_TLS_CERT_FILE: secrets/cert.pem
      OP_TLS_KEY_FILE: secrets/key.pem
      OP_SESSION: eyblahblah
      XDG_DATA_HOME: /config
    ports:
      - "192.168.123.150:443:443"
    image: docker.io/1password/connect-api:latest
    restart: unless-stopped
    volumes:
      - data:/config
      - ./secrets:/secrets
  onepassword-connect-sync:
    container_name: onepassword-connect-sync
    environment:
      OP_SESSION: eyblahblah
      XDG_DATA_HOME: /config
    image: docker.io/1password/connect-sync:latest
    restart: unless-stopped
    volumes:
      - data:/config
volumes:
  data:
    driver: local
    driver_opts:
      device: tmpfs
      o: uid=999,gid=999
      type: tmpfs
```