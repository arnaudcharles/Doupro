# CLI

Everything you can do in the web UI, you can also do from the command
line — both talk to the exact same API, so they never behave differently.

## From inside the container

```bash
docker exec -it doupro doupro containers list
docker exec -it doupro doupro update adguard-home
docker exec -it doupro doupro rollback adguard-home
docker exec -it doupro doupro schedule create --stack media --policy delayed --after 24h
docker exec -it doupro doupro logs --follow
```

## From another machine

Create an API key in **Settings → Security**, then:

```bash
export DOUPRO_HOST=https://doupro.home.arpa
export DOUPRO_API_KEY=your-key-here

doupro containers list
doupro update adguard-home
```

Add `--json` to any list/read command for scriptable output. Run any
`doupro` command with `--help` for the full flag reference.
