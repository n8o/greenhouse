workspace "greenhouse" "A software factory that fits within one person's Claude Max subscription." {

  model {
    human = person "Maintainer" "Writes the plan, reviews and merges every ready PR."

    sys = softwareSystem "greenhouse" {

      cli = component "cli" "greenhouse command: run / tick / status / report." {
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
      gate = component "gate" "Quota / WIP / open-PR gates. Deterministic." {
        properties { paths "internal/gate/**" }
      }
      launch = component "launch" "Starts stage sessions via agent-deck and collects their completions." {
        properties { paths "internal/launch/**" }
      }
      verify = component "verify" "Runs configured deterministic checks on a PR branch." {
        properties { paths "internal/verify/**" }
      }
      ledger = component "ledger" "Append-only JSONL of stage runs + report roll-up." {
        properties { paths "internal/ledger/**" }
      }
      prompts = component "prompts" "Versioned stage prompt templates." {
        properties { paths "prompts/**" }
      }

      cli -> config "loads"
      cli -> plan "reads/advances beads"
      cli -> stage "decides next action"
      cli -> gate "checks before launch"
      cli -> launch "starts stage sessions"
      cli -> verify "runs checks"
      cli -> ledger "records runs"
      config -> stage "names LLM stages"
      launch -> prompts "renders"
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
