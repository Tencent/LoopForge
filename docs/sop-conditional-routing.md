# SOP conditional routing

Conditional routing lets an Agent or script select which downstream branches
run. In the SOP designer, select a node or one of its outgoing connections and
configure the conditions in **分支与汇合**. Existing unconditional edges keep
their normal behavior.

Each conditional edge uses one of these forms:

- **Route label:** match one or more labels emitted by the source node. A label
  may occur on only one outgoing edge of a node. An Agent submits `route` as a
  string or an array of strings in `submit_gate_result`; the available labels
  are included in its prompt. A script writes a JSON object to the path in
  `LOOPFORGE_ROUTE_FILE`, for example:

  ```sh
  printf '%s\n' '{"route":"small"}' > "$LOOPFORGE_ROUTE_FILE"
  ```

- **JSON artifact:** declare the source node's artifact output, then select its
  name, a JSON Pointer (such as `/size`), and a JSON scalar to compare. A
  missing artifact or field does not match. Invalid JSON fails the node.

Multiple conditions can match. Unconditional edges continue to run, except for
an edge designated as the default: that edge is selected only when none of the
conditional edges matches. With no match and no default the source node fails
with its available and submitted route labels. Repository-scoped script route
outputs and repository-scoped artifact conditions are currently rejected
because they could produce conflicting decisions across repositories; use a
run-scoped routing node.

Unselected branches become **skipped** without executing. The default join
rule, **all_success**, skips a node when any upstream branch is skipped. For a
join that should continue after at least one successful branch, choose
**none_failed_min_one_success**. The designer warns about conditional joins
that still use the default rule.

Retrying or sending work back to an upstream node evaluates its conditions
again. Results from an old branch remain in the run history marked stale and
are excluded from downstream artifact inputs. Changing routes does not undo
files that a previously executed branch already wrote in a shared workspace.
Route decisions are recorded as `route.decided` telemetry events.
