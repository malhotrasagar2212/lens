# lens

**See the downstream blast radius of a magic-modules PR before you merge it.**

`lens` points at a [magic-modules](https://github.com/GoogleCloudPlatform/magic-modules) pull request and tells you what it actually does to the generated Terraform provider: how many source files fanned out into how many generated files, which downstream files changed, and which of those changes alter provider behavior (ForceNew flips, required/optional changes, enum changes, and more).

## The problem

A magic-modules PR is small, but the generated provider diff can be huge. A one-line tweak to a shared template can regenerate hundreds of files across dozens of services. Reviewers see a handful of source changes, while the real impact is buried in `terraform-provider-google` and `terraform-provider-google-beta` diffs that are too large to read.

`lens` answers three questions:

1. **How big is the fan-out?** N source files became M generated files.
2. **What changed downstream?** Every changed file, categorized, with `+/-` counts. Large groups are summarized by service.
3. **Does anything break users?** Generated SDKv2 resource schemas are statically analyzed to flag behavioral changes: a field becoming `ForceNew` (destroy/recreate), becoming required, losing an enum value, and so on.

Optionally (`--ai`), it groups downstream files by the source change that triggered them and gives a one-line reason for each.

## Install / build

Requirements: **Go 1.21+**. There are no third-party dependencies, only the standard library.

```bash
git clone <this repo> lens
cd lens
go build -o lens .
```

## Authentication

| Mode | Requirement |
|------|-------------|
| `lens pr <number>` | `GITHUB_TOKEN` env var with read access to public repos |
| `lens pr <number> --ai` | A **fine-grained** token with the **"Copilot Requests"** permission, plus the [GitHub Copilot CLI](https://docs.github.com/copilot) (`copilot`) on your `PATH` |
| Local modes | No token needed |

```bash
export GITHUB_TOKEN=github_pat_xxx
```

`--ai` is routed through the GitHub Copilot CLI (`copilot -p ... -s`), not a direct model API, so it stays within policy. If the Copilot call fails, `lens` prints the error to stderr and still shows the full report.

## Usage

```text
lens pr <NUMBER> [--json] [--beta] [--ai]
lens [--json] <baseDir> <headDir>
lens [--json] <base.go> <head.go>
```

### Modes

| Command | What it does |
|---------|--------------|
| `lens pr <number>` | Analyze a magic-modules PR end to end |
| `lens <baseDir> <headDir>` | Diff two directories of generated provider code (`resource_*.go` and `data_source_*.go`) |
| `lens <base.go> <head.go>` | Diff two individual generated resource files |

### Flags

| Flag | Applies to | Description |
|------|-----------|-------------|
| `--beta` | `pr` | Analyze the `terraform-provider-google-beta` diff instead of GA (`terraform-provider-google`) |
| `--json` | all | Emit machine-readable JSON instead of the text report |
| `--ai` | `pr` | Group downstream files by triggering source change, with a one-line reason each (via Copilot CLI) |

Flags can appear anywhere on the command line.

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | No breaking changes found |
| `1` | At least one **BREAKING** change found (handy for CI gating), or `GITHUB_TOKEN` is missing |
| `2` | Usage error or failure (bad args, unreadable file, API error) |

### Examples

```bash
# Analyze a PR against the GA provider
lens pr 12345

# Analyze the beta provider, with AI grouping
./lens pr 12345 --beta --ai

# JSON for scripting
./lens pr 12345 --json | jq '.changes[] | select(.severity == 2)'

```

## Example output

```text
================================================================
 lens - downstream blast radius
================================================================
  repo    : modular-magician/terraform-provider-google
  fan-out : 3 magic-modules file(s) -> 214 generated file(s)

 CHANGED FILES
  [resource]:
    ~ google/services/compute/resource_compute_instance.go   +12/-3
    ~ google/services/compute/resource_compute_disk.go       +4/-4

  [test]:
    187 files across: compute(92), container(41), bigquery(28), storage(26)

  [docs]:
    ~ website/docs/r/compute_instance.html.markdown          +6/-0
    ~ website/docs/r/compute_disk.html.markdown              +2/-2

  [registration]:
    ~ google/provider/provider_mmv1_resources.go             +2/-0

 BEHAVIORAL CHANGES (resource schemas)
   [BREAKING] (2)
    - `network_interface.subnetwork` is now ForceNew - changing it DESTROYS and recreates the resource
    - `machine_type` no longer accepts value: `n1-legacy` - configs using it break

   [REVIEW] (1)
    - `boot_disk.mode` default changed from `"READ_ONLY"` to `"READ_WRITE"`

   [SAFE/ ADDITIVE] (2)
    - added optional field `scheduling.max_run_duration` (TypeList)
    - `labels` accepts new value: `PREEMPTIBLE`
```

With `--ai`, a section like this follows:

```text
 WHY THESE CHANGED
   - mmv1/templates/terraform/resource.go.tmpl: added a nil-check to the generated
     Read func, so every generated resource and its test regenerated
     (compute 92, container 41, bigquery 28, storage 26)
   - mmv1/products/compute/Instance.yaml: subnetwork marked immutable, so only
     compute_instance and its docs changed
```

In local modes the report is headed "Generated Behavioral diff" and shows the changed resources, the approximate lines of generated Go that changed, and the same BREAKING / REVIEW / SAFE sections.

## What gets flagged

| Severity | Examples |
|----------|----------|
| **BREAKING** | Field now `ForceNew`; field now required; new required field; field removed; field type changed; enum value removed; import support removed; resource removed |
| **REVIEW** | Default changed; field now computed; `MaxItems` tightened; field no longer sensitive; import ID format changed (best-effort) |
| **SAFE / ADDITIVE** | New optional or output-only field; no longer `ForceNew`; now optional; enum value added; now sensitive; import support added |

Nested blocks (`Elem: &schema.Resource{...}`) are compared recursively, and fields are reported by dotted path.

## How it works

1. **Find the diff report.** `lens` scans the PR's comments for the `modular-magician` bot's compare link (`.../terraform-provider-google[-beta]/compare/<base>...<head>`). It prefers the link that matches the PR's current head commit, and falls back to the most recently updated link. `--beta` picks the beta provider.
2. **Reconstruct the true base→head diff.** The bot's compare is three-dot, so it can include unrelated upstream commits. `lens` looks at the merge base and uses it to drop files that changed only upstream, so the result is the PR's own downstream effect.
3. **Count the fan-out.** Source files come from the PR's files API in magic-modules. Generated files come from the reconstructed diff.
4. **Categorize files.** Each downstream file is tagged `resource`, `data source`, `list resource`, `action`, `registration`, `test`, `docs`, `meta`, or `other`. Groups over 12 files are collapsed into a per-service summary (e.g. `compute(92), container(41)`).
5. **Analyze schemas statically.** For changed `resource_*.go` files, `lens` fetches the file at the base and head commits and parses it with `go/parser`. It finds each `schema.Resource{...}` literal and extracts, per field: type, `Required`/`Optional`/`Computed`, `ForceNew`, `Sensitive`, `MaxItems`, `Default`, `validation.StringInSlice` enums, nested schemas, and import support and ID formats. It then diffs the two specs and classifies each difference by severity. Nothing is executed or compiled.
6. **(Optional) Explain with AI.** With `--ai`, source patches (each truncated to 2,000 chars) and the downstream file list are sent to `copilot -p ... -s`. Changes to shared templates are clubbed under one reason; per-resource changes get their own.

The local modes skip steps 1–4 and run only the schema analysis on files you provide.

## Limitations

- **Behavioral analysis is SDKv2-only.** Only `schema.Resource` literals are parsed. Plugin Framework resources (`resource.Schema`, attributes, plan modifiers) are listed as changed files but get no behavioral analysis.
- **Static analysis only.** Schemas built dynamically (helper functions, variables, spread maps) and validators other than inline `StringInSlice` literals aren't understood. Absence of a flag doesn't prove safety.
- **Modified-file counts can read slightly high.** They come from the bot's three-dot compare data. The merge-base filtering removes most unrelated upstream files, but some noise can remain.
- **The GitHub compare API shows at most 300 files per page.** Very large PRs, such as shared-template changes, may be truncated, so fan-out numbers for those PRs are lower bounds. `lens` does not paginate the compare endpoint.
- **Requires a posted diff report.** If the modular-magician bot hasn't commented yet (or the PR predates it), `lens pr` exits with an error.
- **Source-file listing is capped by pagination behavior and API errors**, which are silently ignored, so a transient failure can undercount source files.
- **`--ai` output is a model's summary.** It's a reading aid, not ground truth, and it is skipped (with a warning) if the Copilot CLI is missing or the token lacks the "Copilot Requests" permission.
- **ID-format change detection is best-effort.** It's a string-literal heuristic.
- **Directory mode** only compares `resource_*.go` and `data_source_*.go` files.