#!/usr/bin/env bash
set -euo pipefail

CONTAINER_NAME="spicedb-gotoni"
GRPC_PORT=50061
HTTP_PORT=8444
PRESHARED_KEY="${SPICEDB_PRESHARED_KEY:-gotoni_dev_key}"

SCHEMA='definition user {}

definition organization {
    relation admin: user
    relation editor: user
    relation reader: user
    relation member: user

    permission admin_access = admin
    permission write_access = admin + editor
    permission read_access = admin + editor + reader
}

definition project {
    relation org: organization
    relation editor: user
    relation reader: user

    permission delete = org->admin_access
    permission edit = editor + org->write_access
    permission view = editor + reader + org->read_access
}

definition cluster {
    relation project: project
    relation owner: user

    permission delete = owner + project->delete
    permission edit = owner + project->edit
    permission view = owner + project->view
}

definition resource {
    relation project: project
    relation parent_cluster: cluster
    relation owner: user

    permission delete = owner + parent_cluster->edit + project->delete
    permission edit = owner + parent_cluster->edit + project->edit
    permission ssh = edit
    permission view = owner + parent_cluster->view + project->view
}

definition ssh_key {
    relation resource: resource
    relation owner: user

    permission delete = owner + resource->edit
    permission use = owner + resource->ssh
    permission view = owner + resource->view
}'

# Stop existing container if running
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping existing gotoni SpiceDB container..."
    docker rm -f "$CONTAINER_NAME"
fi

echo "Starting SpiceDB for gotoni (in-memory datastore)..."
docker run -d \
    --name "$CONTAINER_NAME" \
    -p "${GRPC_PORT}:50051" \
    -p "${HTTP_PORT}:8443" \
    authzed/spicedb:latest \
    serve \
    --grpc-preshared-key "$PRESHARED_KEY" \
    --http-enabled \
    --datastore-engine memory

echo "Waiting for SpiceDB to start..."
for i in $(seq 1 10); do
    if curl -sf -o /dev/null "http://localhost:${HTTP_PORT}/healthz" 2>/dev/null; then
        break
    fi
    sleep 1
done

echo "Writing Crusoe Cloud schema..."
curl -s -X POST "http://localhost:${HTTP_PORT}/v1/schema/write" \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${PRESHARED_KEY}" \
    -d "$(python3 -c "import json; print(json.dumps({'schema': '''$SCHEMA'''}))")" | python3 -m json.tool

echo ""
echo "=== SpiceDB for gotoni is ready ==="
echo "  gRPC:  localhost:${GRPC_PORT}"
echo "  HTTP:  localhost:${HTTP_PORT}"
echo "  Key:   ${PRESHARED_KEY}"
echo ""
echo "Verify: curl -s -X POST http://localhost:${HTTP_PORT}/v1/schema/read -H 'Content-Type: application/json' -H 'Authorization: Bearer ${PRESHARED_KEY}' -d '{}'"
echo "Stop:   docker rm -f ${CONTAINER_NAME}"
