#!/usr/bin/env bash
# Local-only R1-1 source/OCI evidence. No Registry, GitHub Release or Channel writes.
# CPAMP_CANDIDATE_LOAD=1 additionally loads each verified archive into the local
# engine and asserts its image ID equals the recorded imageConfigDigest. The load
# step never rebuilds and never substitutes another candidate source.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
if [[ $# -ne 1 ]]; then
  echo "Usage: bash bin/release/build-embedded-candidate.sh ABSOLUTE_EMPTY_OUTPUT_DIR" >&2
  exit 2
fi
output="$1"
if [[ "$output" != /* ]]; then
  echo "Use an absolute empty output directory outside the repository." >&2
  exit 2
fi
if [[ -e "$output" ]]; then
  if [[ ! -d "$output" ]] || [[ -n "$(find "$output" -mindepth 1 -print -quit)" ]]; then
    echo "STOP: OCI output directory must be empty." >&2
    exit 2
  fi
fi
# Never allow the output to enter Docker build context on subsequent roles.
python3 - "$PWD" "$output" <<'PY'
import pathlib, sys
source = pathlib.Path(sys.argv[1]).resolve()
destination = pathlib.Path(sys.argv[2]).resolve()
if destination == source or source in destination.parents:
    raise SystemExit("STOP: OCI output must be outside the source tree")
if destination.exists() and not destination.is_dir():
    raise SystemExit("STOP: OCI output path is not a directory")
PY
if [[ -n "$(git status --porcelain)" ]]; then
  echo "STOP: build requires a clean source tree." >&2
  exit 1
fi
source_sha="$(git rev-parse --verify HEAD)"
event="local-r1-$(python3 -c 'import uuid; print(uuid.uuid4().hex)')"
docker buildx version >/dev/null
# Final Go binary/module identity is read without execution by go version -m.
go version >/dev/null

# The OCI exporter needs a container-driver builder: the docker driver rejects
# `type=oci`. Select the builder explicitly instead of trusting an ambient
# default, so a run can never silently export a different candidate format.
builder_args=()
if [[ -n "${CPAMP_CANDIDATE_BUILDER:-}" ]]; then
  if ! docker buildx inspect "$CPAMP_CANDIDATE_BUILDER" >/dev/null 2>&1; then
    echo "STOP: buildx builder not found: $CPAMP_CANDIDATE_BUILDER" >&2
    exit 1
  fi
  builder_args=(--builder "$CPAMP_CANDIDATE_BUILDER")
elif [[ "$(docker buildx inspect 2>/dev/null | awk '/^Driver:/ {print $2; exit}')" == "docker" ]]; then
  echo "STOP: the default buildx builder uses the docker driver, which cannot export an OCI layout." >&2
  echo "STOP: select a container builder with CPAMP_CANDIDATE_BUILDER=<name>." >&2
  exit 1
fi

# Materialize one commit-addressed, disposable context. All three builds and
# the final verifier consume this same tree; the caller's working tree cannot
# change the bytes between Manager, Ingress and Runtime builds.
snapshot="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-r1-context.XXXXXXXX")"
cleanup() {
  chmod -R u+w "$snapshot" >/dev/null 2>&1 || :
  rm -rf -- "$snapshot"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 - "$PWD" "$snapshot" <<'PY'
import pathlib, sys
source = pathlib.Path(sys.argv[1]).resolve()
snapshot = pathlib.Path(sys.argv[2]).resolve()
if snapshot == source or source in snapshot.parents:
    raise SystemExit("STOP: candidate snapshot must be outside the source tree")
PY
git archive --format=tar "$source_sha" | tar -xf - -C "$snapshot"
chmod -R a-w "$snapshot"

# Stamped labels are candidate source claims, NOT signed producer provenance.
mkdir -p "$output"

for role in manager ingress runtime; do
  case "$role" in
    manager) file=Dockerfile.manager-server; target=; ;;
    ingress) file=Dockerfile.ingress; target=; ;;
    runtime) file=Dockerfile.runtime; target=runtime; ;;
  esac
  args=(
    --platform linux/amd64
    --build-arg "SOURCE_COMMIT=$source_sha"
    --build-arg VERSION=2.0.0-r1candidate
    --tag "cpamp-r1-$role:candidate"
    --label "org.opencontainers.image.revision=$source_sha"
    --label "org.opencontainers.image.source=https://github.com/seakee/CPA-Manager-Plus"
    --label "org.opencontainers.image.version=2.0.0-r1candidate"
    --label "io.cpamp.candidate.event=$event"
    --label "io.cpamp.candidate.role=$role"
    --provenance=false
    --sbom=false
    # The OCI archive is the verified candidate evidence. The docker-format tar
    # is a load transport for the very same build result: both outputs share the
    # config and layer blobs, and the engine cannot load an OCI layout archive.
    --output "type=oci,name=cpamp-r1-$role:candidate,dest=$output/$role.oci.tar"
    --output "type=docker,dest=$output/$role.docker.tar"
  )
  if [[ -n "$target" ]]; then args+=(--target "$target"); fi
  # `${arr[@]+...}` keeps the empty array legal under `set -u` in bash 3.2.
  docker buildx build ${builder_args[@]+"${builder_args[@]}"} "${args[@]}" \
    --file "$snapshot/$file" "$snapshot"
done

python3 "$snapshot/bin/release/verify-embedded-candidate.py" \
  --source-root "$snapshot" --source-commit "$source_sha" --build-event "$event" \
  --manager "$output/manager.oci.tar" \
  --ingress "$output/ingress.oci.tar" \
  --runtime "$output/runtime.oci.tar" \
  --out "$output/candidate-evidence.json"

if [[ -d "$PWD/node_modules" ]]; then
  export NODE_PATH="$PWD/node_modules${NODE_PATH:+:$NODE_PATH}"
fi
node "$snapshot/bin/release/render-embedded-compose.mjs" \
  --evidence "$output/candidate-evidence.json" \
  --compose-out "$output/docker-compose.v2-embedded.yml" \
  --pairing-out "$output/embedded-release-pairing.json"

python3 "$snapshot/bin/release/verify-embedded-candidate.py" \
  --evidence "$output/candidate-evidence.json" \
  --manager "$output/manager.oci.tar" \
  --ingress "$output/ingress.oci.tar" \
  --runtime "$output/runtime.oci.tar" \
  --verify-compose "$output/docker-compose.v2-embedded.yml" \
  --verify-pairing "$output/embedded-release-pairing.json"

# Opt-in local load. The OCI archive stays the verified evidence artifact; the
# engine loads the docker-format tar exported by the SAME build invocation. The
# loaded image ID must equal the recorded imageConfigDigest, and the transport's
# config/layer blobs must match the verified OCI archive; any mismatch is a hard
# failure with no fallback build.
if [[ "${CPAMP_CANDIDATE_LOAD:-0}" == "1" ]]; then
  load_records=()
  for role in manager ingress runtime; do
    reference="cpamp-r1-$role:candidate"
    if ! docker load --input "$output/$role.docker.tar" >/dev/null; then
      echo "STOP: docker load rejected the build transport for $role." >&2
      exit 1
    fi
    expected_id="$(python3 - "$output/candidate-evidence.json" "$role" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    print(json.load(handle)["images"][sys.argv[2]]["imageConfigDigest"])
PY
)"
    actual_id="$(docker image inspect --format '{{.Id}}' "$reference" 2>/dev/null || true)"
    if [[ -z "$actual_id" ]]; then
      echo "STOP: the build transport did not carry the reference $reference." >&2
      exit 1
    fi
    if [[ "$actual_id" != "$expected_id" ]]; then
      echo "STOP: loaded image identity differs from the verified candidate for $role." >&2
      echo "STOP: expected $expected_id, loaded $actual_id." >&2
      exit 1
    fi
    record="$(python3 - "$output/$role.oci.tar" "$output/$role.docker.tar" "$role" "$expected_id" "$actual_id" <<'PY'
import json, sys, tarfile

oci_path, transport_path, role, expected_id, actual_id = sys.argv[1:6]


def member(path, name):
    with tarfile.open(path) as archive:
        return archive.extractfile(name).read()


def blob_path(digest):
    algorithm, value = digest.split(":", 1)
    return "blobs/" + algorithm + "/" + value


index = json.loads(member(oci_path, "index.json"))
descriptors = index.get("manifests") or []
assert len(descriptors) == 1, "verified OCI archive must hold one manifest"
manifest = json.loads(member(oci_path, blob_path(descriptors[0]["digest"])))
oci_config = manifest["config"]["digest"]
oci_layers = [layer["digest"] for layer in manifest["layers"]]
transported = json.loads(member(transport_path, "manifest.json"))
assert len(transported) == 1, "build transport must hold one image"
entry = transported[0]
transport_config = entry["Config"].removeprefix("blobs/").split("/", 1)[1]
transport_config = "sha256:" + transport_config
transport_layers = ["sha256:" + layer.removeprefix("blobs/").split("/", 1)[1]
                    for layer in entry["Layers"]]
assert expected_id == actual_id == oci_config == transport_config, "config digest mismatch"
assert transport_layers == oci_layers, "layer digest mismatch"
print(json.dumps({
    "role": role,
    "reference": "cpamp-r1-" + role + ":candidate",
    "imageId": actual_id,
    "imageConfigDigest": expected_id,
    "configBlobMatchesOCI": True,
    "layerDigestsMatchOCI": True,
    "layers": len(oci_layers),
    "transport": "docker-format export of the same buildx invocation",
}, sort_keys=True))
PY
)"
    load_records+=("$record")
    printf 'LOAD PASS %s %s\n' "$reference" "$actual_id"
  done
  python3 - "$output/load-evidence.json" "${load_records[@]}" <<'PY'
import json, sys
records = [json.loads(value) for value in sys.argv[2:]]
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump({
        "schemaVersion": 1,
        "status": "LOCAL_LOAD_IDENTITY_ONLY_NOT_RELEASE_QUALIFIED",
        "note": "docker-format tar is a load transport for the same build; the OCI archive remains the verified evidence",
        "images": {record["role"]: record for record in records},
    }, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
  chmod 600 "$output/load-evidence.json"
fi
echo "STOP: local OCI evidence is NOT a signed production receipt or release."
