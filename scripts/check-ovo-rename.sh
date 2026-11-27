#!/bin/bash
# Three-generation rename regression: fake Docker and init services only.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work_root="${TMPDIR:-$ROOT/.git/tmp}"
mkdir -p "$work_root"
work=$(mktemp -d "$work_root/ovo-rename.XXXXXX")
trap 'rm -rf "$work"' EXIT
load() { source <(awk '/^main\(\)/ {exit} {print}' "$ROOT/$1"); }
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
(
  load panel_install.sh
  for ref in ghcr.io/yhfc1775-maker/ovo-panel-backend:2.1.34 ghcr.io/iiiiiiiii7125-art/flux-panel-backend:2.1.29 ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:2.1.29 ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:2.1.29 ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:2.1.29; do
    panel_image_ref_owned "$ref" || fail "ownership positive $ref"
  done
  for ref in ghcr.io/iiiiiiiii7125-art/ovo-panel-backend:2.1.34 ghcr.io/other/flmx-panel-backend:2.1.29 ghcr.io/iiiiiiiii7125-art/unrelated:2.1.29; do
    if panel_image_ref_owned "$ref"; then fail "ownership negative $ref"; fi
  done
  pass 'image owner and generation pairing (positive and negative)'
)
for generation in flux flmx ovo; do
  (
    load panel_install.sh
    dir="$work/$generation"; mkdir -p "$dir"
    case "$generation" in
      flux) backend=flux-panel-backend; frontend=vite-frontend; pg=flux-panel-postgres; owner=iiiiiiiii7125-art ;;
      flmx) backend=flmx-panel-backend; frontend=flmx-panel-frontend; pg=flmx-panel-postgres; owner=iiiiiiiii7125-art ;;
      ovo) backend=ovo-panel-backend; frontend=ovo-panel-frontend; pg=ovo-panel-postgres; owner=yhfc1775-maker ;;
    esac
    image="ghcr.io/$owner/$backend:2.1.29"; [[ "$generation" != ovo ]] || image="ghcr.io/$owner/$backend:2.1.34"
    cat > "$dir/docker-compose.yml" <<EOF
services:
  backend:
    image: $image
    container_name: $backend
  frontend:
    image: ghcr.io/$owner/$frontend:2.1.29
  postgres:
    image: postgres:16
EOF
    cat > "$dir/.env" <<EOF
PANEL_BACKEND_CONTAINER=$backend
PANEL_FRONTEND_CONTAINER=$frontend
PANEL_POSTGRES_CONTAINER=$pg
FLMX_VERSION=2.1.29
FLMX_INSTALL_ID=preserved-uuid
FLMX_SQLITE_VOLUME=existing-sqlite
FLMX_POSTGRES_VOLUME=existing-pg
FLMX_NETWORK=existing-network
COMPOSE_PROJECT_NAME=existing-project
EOF
    STUB_BACKEND="$backend" STUB_IMAGE="$image" STUB_DIR="$dir"
    docker() {
      if [[ "$1" == ps ]]; then printf '%s\n' "$STUB_BACKEND"; return; fi
      [[ "$1" == inspect ]] || return 0
      if [[ "${2:-}" == -f ]]; then
        case "$3" in
          '{{.Config.Image}}') [[ "$4" == "$STUB_BACKEND" ]] || return 1; printf '%s' "$STUB_IMAGE" ;;
          *project.working_dir*) printf '%s' "$STUB_DIR" ;;
          *project\"*) printf '%s' existing-project ;;
        esac
        return 0
      fi
      [[ "$2" == "$STUB_BACKEND" ]]
    }
    printf 'STUB %s container=%s image=%s compose_working_dir=%s compose_project=existing-project compose_service=backend\n' "$generation" "$backend" "$image" "$dir"
    cd "$dir"
    for mode in cwd explicit arbitrary; do
      for operation in update uninstall migrate; do
        (
          unset PANEL_BACKEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_DEPLOY_DIR COMPOSE_PROJECT_NAME
          case "$mode" in
            cwd) cd "$dir" ;;
            explicit) cd "$work"; export PANEL_BACKEND_CONTAINER="$backend" ;;
            arbitrary) cd "$work" ;;
          esac
          resolve_panel_deployment >/dev/null || fail "$generation $mode $operation resolution gate"
          [[ "$PANEL_BACKEND_CONTAINER" == "$backend" && "$PANEL_DEPLOY_DIR" == "$dir" ]] || fail 'wrong resolved target'
        )
      done
      pass "$generation $mode update/uninstall/migrate resolution gates"
    done
    # The original configuration is the rollback source, as in the update flow.
    cp -p .env .env.before
    panel_pin_missing_resource_keys() { :; }
    panel_stop_remove_container() { :; }
    PANEL_BACKEND_CONTAINER="$backend" PANEL_FRONTEND_CONTAINER="$frontend" PANEL_POSTGRES_CONTAINER="$pg"
    apply_panel_identity_rename >/dev/null
    [[ "$(get_env_var PANEL_BACKEND_CONTAINER)" == ovo-panel-backend ]] || fail 'identity not renamed'
    [[ "$(get_env_var OVO_INSTALL_ID)" == preserved-uuid ]] || fail 'UUID changed'
    [[ "$(get_env_var OVO_SQLITE_VOLUME)" == existing-sqlite ]] || fail 'volume changed'
    if grep -q '^FLMX_INSTALL_ID=' .env; then fail 'old key was not migrated'; fi
    cp -p .env.before .env
    cmp -s .env .env.before || fail 'rollback bytes changed'
    pass "$generation identity migration, UUID/volume preservation and configuration rollback"
  )
done
# Direct agent migration and its real rollback body, each with a fake init manager.
for generation in flux flmx; do
  for outcome in success rollback snapshot-failure; do
    (
      load install.sh
      dir="$work/agent-$generation-$outcome"; mkdir -p "$dir"
      INSTALL_DIR="$dir/ovo_agent"; LEGACY_INSTALL_DIR="$dir/flux_agent"; LEGACY2_INSTALL_DIR="$dir/flmx_agent"
      OVO_AGENT_SYSTEMD_SERVICE_FILE="$dir/ovo.service"; OVO_AGENT_OPENRC_SERVICE_FILE="$dir/ovo.init"
      LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$dir/flux.service"; LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE="$dir/flux.init"
      LEGACY_FLMX_AGENT_SYSTEMD_SERVICE_FILE="$dir/flmx.service"; LEGACY_FLMX_AGENT_OPENRC_SERVICE_FILE="$dir/flmx.init"
      SERVICE_MANAGER=systemd
      case "$generation" in flux) old="$LEGACY_INSTALL_DIR"; unit="$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE"; migration=migrate_legacy_agent_install ;; flmx) old="$LEGACY2_INSTALL_DIR"; unit="$LEGACY_FLMX_AGENT_SYSTEMD_SERVICE_FILE"; migration=migrate_legacy_flmx_agent_install ;; esac
      mkdir -p "$old"; printf 'binary-%s\n' "$generation" > "$old/${generation}_agent"; printf '{"addr":"wss://panel.example"}\n' > "$old/config.json"
      printf 'original-unit-%s\n' "$generation" > "$unit"; cp -p "$unit" "$dir/unit.before"
      ensure_service_manager() { :; }; systemctl() { return 0; }; sleep() { :; }
      ovo_agent_service_exists() { [[ -f "$OVO_AGENT_SYSTEMD_SERVICE_FILE" ]]; }
      legacy_flux_agent_service_exists() { [[ -f "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" ]]; }
      legacy_flmx_agent_service_exists() { [[ -f "$LEGACY_FLMX_AGENT_SYSTEMD_SERVICE_FILE" ]]; }
      write_ovo_agent_service() { printf new-unit > "$OVO_AGENT_SYSTEMD_SERVICE_FILE"; }
      ovo_agent_health_ok() { [[ "$outcome" == success ]]; }
      legacy_flux_agent_health_ok() { return 0; }; legacy_flmx_agent_health_ok() { return 0; }
      if [[ "$outcome" == snapshot-failure ]]; then cp() { return 1; }; fi
      rc=0; "$migration" > "$dir/result.log" 2>&1 || rc=$?
      if [[ "$outcome" == success ]]; then
        [[ "$rc" == 0 && -f "$INSTALL_DIR/ovo_agent" && ! -e "$old" ]] || { cat "$dir/result.log"; fail "$generation success"; }
      else
        [[ "$rc" != 0 && -f "$old/${generation}_agent" ]] || { cat "$dir/result.log"; fail "$generation $outcome"; }
        cmp -s "$unit" "$dir/unit.before" || fail "$generation old unit was not restored byte for byte"
      fi
      pass "$generation direct agent migration $outcome"
    )
  done
done
(
  load install.sh
  unit="$work/kernel.service"
  printf 'Environment=OVO_KERNEL_DIR=/current/kernels\nEnvironment=FLMX_KERNEL_DIR=/previous/kernels\n' > "$unit"
  [[ "$(configured_kernel_dir_for "$unit")" == /current/kernels ]] || fail 'OVO kernel directory must win regardless of line order'
  printf 'Environment=FLMX_KERNEL_DIR=/previous/kernels\n' > "$unit"
  [[ "$(configured_kernel_dir_for "$unit")" == /previous/kernels ]] || fail 'legacy kernel directory must remain readable'
  pass 'kernel directory current-key priority and previous-key fallback'
)
(
  load panel_install.sh
  dir="$work/pg-url"; mkdir -p "$dir"; cd "$dir"
  printf 'DATABASE_URL=postgresql://flmx%%5Fpanel:test-pass@postgres:5432/flmx_panel?sslmode=disable\n' > .env
  PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
  docker() { return 1; }
  [[ "$(panel_postgres_url_identity POSTGRES_USER)" == flmx_panel ]] || fail 'URL user decoding'
  [[ "$(panel_postgres_url_identity POSTGRES_DB)" == flmx_panel ]] || fail 'URL database decoding'
  panel_pin_postgres_identity
  [[ "$(get_env_var POSTGRES_USER)" == flmx_panel && "$(get_env_var POSTGRES_DB)" == flmx_panel ]] || fail 'existing PostgreSQL identity changed'
  printf 'DB_TYPE=postgres\n' > .env
  if panel_pin_postgres_identity >/dev/null 2>&1; then fail 'unknown existing PostgreSQL identity must fail closed'; fi
  pass 'PostgreSQL URL identity preserved; unknown identity fails closed'
)
pass 'all three-generation rename regression cases'
