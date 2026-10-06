# CRD at 7c21646

The Agent CRD from before #278's embedding rule, as `git show 7c21646:config/crd/bases/agent.garam.sh_agents.yaml` prints it. The CRD upgrade tests in `internal/garam/constructor` and `internal/controller` install it, store an `Agent`, then swap in `config/crd/bases` (#289).
