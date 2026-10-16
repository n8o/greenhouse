workspace "greenhouse" "A software factory that fits within one person's Claude Max subscription." {

  model {
    human = person "Maintainer" "Writes the plan, reviews and merges every ready PR."

    sys = softwareSystem "greenhouse" {

      cli = component "cli" "greenhouse command and scheduler: tick / run (lock, ledger, gh adapter) / status / doctor / report." {
        properties { paths "cmd/greenhouse/**" }
      }
      config = component "config" "greenhouse.toml: repo, plan, caps, quota thresholds, verify commands." {
        properties { paths "internal/config/**" }
      }
      plan = component "plan" "Beads adapter: reads ready approved beads, reads/writes stage labels via bd." {
        properties { paths "internal/plan/**" }
      }
      stage = component "stage" "Pure state machine: bead stage + event -> next stage/action. No I/O." {
        properties { paths "internal/stage/**" }
      }
      gate = component "gate" "Quota / WIP / open-PR gates: pure decisions over a usage reading, counts and the time, plus read-only agent-deck/gh probes." {
        properties { paths "internal/gate/**" }
      }
      doctor = component "doctor" "Read-only host checks: statusLine ingest, notify-daemon (a warning), tools, MCP approval, stage models." {
        properties { paths "internal/doctor/**" }
      }
      launch = component "launch" "Starts stage sessions via agent-deck and collects their completions." {
        properties { paths "internal/launch/**" }
      }
      verify = component "verify" "Runs configured deterministic checks on a PR branch." {
        properties { paths "internal/verify/**" }
      }
      ledger = component "ledger" "Local run state under .greenhouse/: append-only ledger.jsonl of stage runs, open runs, usage-limit park." {
        properties { paths "internal/ledger/**" }
      }
      report = component "report" "Pure ledger rollup: quota per merged PR, cycle time, parks, retries, rejects, and the scoped-memory join and comparison. No I/O." {
        properties { paths "internal/report/**" }
      }
      memory = component "memory" "Reads nugit's usage log (.nugit/.cache/usage.jsonl) and a stage branch's first commit via git." {
        properties { paths "internal/memory/**" }
      }
      prompts = component "prompts" "Versioned stage prompt templates." {
        properties { paths "prompts/**" }
      }

      cli -> config "loads"
      cli -> plan "reads/advances beads"
      cli -> stage "decides next action"
      cli -> gate "checks before launch"
      cli -> doctor "greenhouse doctor"
      cli -> launch "starts stage sessions"
      cli -> verify "runs checks"
      cli -> ledger "records runs"
      cli -> report "greenhouse report"
      cli -> memory "reads the nugit usage log and first commits"
      report -> ledger "rolls up records"
      report -> stage "reads outcomes as event kinds"
      config -> stage "names LLM stages"
      launch -> prompts "renders"
      launch -> config "reads stage model/effort/turn budget"
      launch -> stage "parses session results into events"
      gate -> config "reads quota and WIP caps"
      gate -> launch "finds implement sessions by group and title"
      gate -> stage "gates WIP and backlog on implement only"
      doctor -> config "reads stage models"
      doctor -> launch "runs the MCP preflight"
      doctor -> gate "reads the usage reading and its freshness"
      verify -> stage "maps results onto verify events"
    }

    human -> sys "plans, reviews, merges"
  }

  views {
    component sys "Components" {
      include *
      autolayout lr
    }
    theme default
  }
}
