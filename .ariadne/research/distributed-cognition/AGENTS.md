# Distributed cognition — research mandate

Open-ended scientific investigation. Not a brainstorm, not a literature
summary, not a speculative design document.

> **The runtime supplies the rest of the prompt.** Your identity, the scope
> notice, the current research state recovered from memory, feedback on any
> denied escalation, live user steering, the bounded-turn instruction, the
> escalation rules, and the output contract are all prepended or appended
> around this file. Nothing here restates them, and nothing here can change
> who you are. This file is the *mandate*: the question, the standard of
> evidence, and the constraints the work must respect.

---

## 1. Objective

The central research question:

> What minimal communication, learning, or coordination mechanism allows
> independently stateful neural components to become a causally integrated
> distributed cognitive system while preserving useful functional
> differentiation between those components?

Find a computational principle, mechanism, or regime that is not a
restatement of existing multi-agent, mixture-of-experts, ensemble, routing,
or distributed-transformer techniques.

The desired outcome is **not** a better benchmark score. It is evidence for
a mechanism that explains how distributed systems integrate information
without collapsing into either:

1. independent components that never form a coherent collective; or
2. heavily communicating components that homogenize into an expensive
   approximation of a monolithic network.

Treat this as a discovery problem.

## 2. Hypothesis space

Do not assume any of these are necessary: global workspaces, transformers,
text or latent-vector communication, shared KV caches, centralized or
learned routing, contract-net coordination, message passing, shared memory,
recurrent specialists, symbolic communication, fixed topology, homogeneous
modules, synchronous computation.

Use them as baselines, priors, or objects of criticism. Concluding that one
is the wrong abstraction is a valid and valuable result.

## 3. The unknown

Given independently stateful modules M₁…Mₙ — private parameters, private
state, partial observations, local computation and learning, limited
bandwidth — no module should initially receive the complete problem state,
so the task genuinely requires distributed computation.

The central unknown is the interaction primitive **C(Mᵢ, Mⱼ, t)**. Do not
assume it is a message. It might be a prediction error, an uncertainty, a
compressed disagreement, a counterfactual, a gradient-like signal, a state
delta, a sparse event, a learned synchronization, an inhibitory signal, a
dynamically formed coalition, an adaptive topology, or something with no
current name. These are illustrations of breadth, not preferences.

## 4. Standard of evidence

Classify every important claim as one of: **established knowledge**,
**inference**, **experimental observation** (from this investigation), or
**speculation**. Never present speculation as fact.

Maintain the evidence ledger as you go — that is what the observations,
hypotheses, and rejected-hypotheses fields of your result are for. Ground
claims in primary literature where possible. Novelty is itself a hypothesis:
actively search for prior art that would disprove it.

## 5. The repository is substrate, not specification

You have a repository of prior work. Treat it as evidence and
infrastructure. Do **not** assume its architecture is correct, necessary,
optimal, or well-framed, and do not preserve a component because effort was
invested in it.

**Before proposing a mechanism**, establish the empirical frontier: what
mechanisms exist, which claims are established, which are untested, which
components are conveniences rather than demonstrated necessities, which
results reproduce, which may introduce confounds, which negative results
already eliminate parts of the search space. Verify a representative subset
rather than trusting summaries. Do not produce a codebase tour.

**Then classify** each major mechanism as *supported*, *under-tested*,
*incidental*, *confounding*, or *candidate for removal*, stating the
evidence. Where mechanisms are tightly coupled, ask whether the effect
reduces to a smaller causal primitive.

**Then choose the cleanest substrate**, freely: extend it, replace parts,
simplify, remove layers, build a minimal independent model, or discard its
assumptions. Prefer the smallest system that tests the hypothesis. Do not
force an experiment into an architecture that obscures causal
interpretation; equally, do not rebuild infrastructure that is already
correct and reproducible without a scientific reason.

## 6. Literature frontier

Reconstruct the frontier across the relevant fields — distributed
transformers, MoE and mixture-of-agents, differentiable routing,
decentralized and multi-agent learning, latent and emergent communication,
modular and recurrent networks, collective world models, information
bottleneck theory, specialization and homogenization, ensemble diversity,
graph computation, neural cellular automata, global workspace, predictive
coding, active inference, sparse coding, developmental learning, lesion
studies, and the neuroscience of integration and specialization.

Do not summarize. Identify contradictions, unexplained results, assumptions
treated as necessary without evidence, mechanisms that work for unclear
reasons, findings that fail under increased communication, structurally
similar phenomena described in different vocabularies across fields,
negative results, missing causal tests, missing ablations, and unexplored
parameter regimes. This feeds hypothesis generation directly.

## 7. Discovery operators

Apply several, not one:

- **Contradiction mining** — find findings resting on incompatible
  assumptions; look for the hidden variable that reconciles them.
- **Assumption inversion** — remove or reverse something treated as
  necessary.
- **Explanatory gap detection** — find A → *unexplained* → B; search for the
  missing mechanism.
- **Cross-domain transfer** — ignore terminology; compare structures.
- **Invariant extraction** — find properties shared by successful
  distributed systems despite architectural differences.
- **Counterexample construction** — build a system that violates the
  accepted explanation but keeps the capability.
- **Phase-transition search** — vary communication, topology, capacity, or
  plasticity continuously and look for qualitative change.

## 8. Substrate and fairness

Prefer small systems that train and test fast — tiny transformers, RNNs,
hybrids, MLP modules with persistent state. Do not start with large language
models unless smaller systems prove inadequate. Each module gets partial
information; the collective must integrate distributed evidence. Good tasks
allow objective scoring, controlled information partitioning, repeatability,
cheap iteration, ablation, lesioning, and communication and topology
manipulation. Do not rely on a single task if the mechanism might be
task-specific.

**Comparisons must be fair.** Control for parameter count, training and
inference compute, bandwidth, memory, observations, data, and optimization
budget. A distributed system that simply receives more resources has
demonstrated nothing. Where exact matching is impossible, quantify the
imbalance.

**Baselines**, as the claim demands: no communication; unrestricted
full-state communication; concatenated latent messages; symbolic messages;
shared workspace; static routing; learned routing; MoE/MoA routing;
independent ensemble; centralized coordinator; parameter-matched monolith.
Not every baseline belongs in every early experiment — strong claims need
the relevant controls.

## 9. What counts as interesting

Task performance alone is insufficient. Measure the qualitative properties:

- **Superadditivity** — does the interacting collective beat what
  independent components or simple ensembling would give? No credit for
  averaging outputs or receiving more compute.
- **Causal communication** — intervene: remove, delay, permute across
  samples, replace with noise, swap senders, or preserve message statistics
  while destroying semantic correspondence. Degradation should match the
  proposed mechanism. If it does not, the channel may not be causal.
- **Differentiation** — representational similarity, activation statistics,
  probing, functional ablation, information measures, contribution, routing
  patterns. Different weights are not specialization.
- **Lesion specificity** — different lesions should produce different
  functional deficits if specialization is real.
- **Recovery** — after lesioning, allow further learning. Distinguish plain
  retraining from redistribution, new specialization, and structural
  reconfiguration.
- **Bottlenecks** — vary capacity continuously; look for smooth degradation,
  saturation, critical thresholds, phase transitions, hysteresis.
- **Homogenization** — more communication is not better. Look explicitly for
  the regime where too little prevents integration, intermediate supports
  coordinated specialization, and too much causes collapse. If it exists,
  explain it mechanistically.

## 10. Look for laws, not architectures

"Performance improves by 4%" is far less interesting than "above a critical
communication density, modules lose representational differentiation and
collective performance declines." Prefer scaling laws, critical thresholds,
topology-dependent regimes, specialization transitions,
communication–diversity trade-offs, recovery laws, attractor regimes, and
coalition dynamics.

## 11. Choosing the next experiment

Prefer the highest expected information gain per unit cost: cheap
discriminating experiments over broad sweeps; causal interventions over
correlations; simple falsifiers over demonstrations; minimal systems over
elaborate ones. Do not spend heavy compute refining a mechanism before
establishing the effect exists.

This is also how escalation is judged. When you report an escalation's
`expected_gain` and `cheaper_test_available`, you are answering exactly this
question, and a cheap discriminating experiment you have not yet run is a
reason to run it rather than to escalate.

## 12. How this programme runs

The earlier form of this mandate said: work continuously, do not stop at a
plan, keep experimenting until you have a result. **That is right about the
programme and wrong about a single turn.**

Ariadne now runs the loop itself. Each invocation performs one bounded step
and returns; the runtime immediately invokes you again with your own
recorded frontier, hypotheses, rejections, and open questions. The
continuity is structural, so it no longer has to be argued for in the
prompt — and a turn that tried to run the whole programme would starve the
scheduler that decides when scarce cognition is worth spending.

What survives from that requirement is the part about the **programme**:

- Do not end the programme after a plan, a repository review, a literature
  review, a list of hypotheses, unexecuted code, one preliminary result, or
  one failed experiment.
- A failed experiment is evidence, not a stopping point. Diagnose it:
  wrong hypothesis, poor implementation, unsuitable task, optimization
  failure, confound, insufficient measurement, wrong parameter regime, or
  resource mismatch — then act on the diagnosis.
- Make reasonable scientific and engineering decisions yourself. Do not ask
  the user to choose between options; steering will arrive if it is wanted.
- If an effect appears, try to break it. If it survives, reproduce it. If it
  reproduces, look for a simpler explanation. If none accounts for it,
  design a stronger test.

Ending a turn is not ending the programme. Ending the programme means
returning `complete` or `blocked`, and the bar for that is section 13.

## 13. When the programme actually ends

Return `complete` only for one of:

**A — Positive result.** A candidate mechanism that is non-trivial; survives
a serious prior-art search; has been concretely implemented and tested;
produces reproducible evidence across seeds; survives at least one genuine
falsification attempt; differs qualitatively from appropriate baselines;
yields a risky new prediction; has a plausible mechanistic explanation; and
is not adequately explained by a simpler known mechanism. Publication-level
proof is not required. More than suggestive anecdote is.

**B — Strong negative result.** The plausible local space has been explored
enough to conclude the tested mechanisms do not produce the phenomenon.
Must state what was tested, why those tests were informative, what failed,
which hypotheses are rejected, what uncertainty remains, which directions
now look least promising, and what experiment would most efficiently extend
the search. Do not manufacture a positive finding.

Return `blocked` only for a hard external limitation you cannot resolve —
unavailable compute, inaccessible data, a tooling failure that makes
experimentation impossible. Attempt alternatives first. Inconvenience is not
a limitation.

## 14. Anti-shortcut rules

Do not: claim success because the distributed model has more parameters;
claim integration because messages exist; claim specialization because
weights differ; claim novelty because terminology differs; infer causation
from correlation; use the task label as evidence of cognitive organization;
cherry-pick seeds; discard negative results unexplained; change metrics
after seeing outcomes; invent post-hoc theory without testing its
consequences; preserve an architecture because it exists; assume a known
distributed architecture contains the answer; or stop at an interesting
result before trying to falsify it.

## 15. The strongest simpler alternative

Whenever a promising interpretation emerges, name the strongest mundane
explanation — extra effective capacity, optimization advantage,
regularization, ensembling, implicit parameter sharing, more training
signal, lucky initialization, information leakage, routing effects, ordinary
bottleneck regularization — and design the experiment that distinguishes
your mechanism from it. Do not claim a new principle until the mundane
explanations have been seriously tested.

## 16. Novelty

For any promising result, run a dedicated novelty investigation that
searches for work which would *invalidate* novelty, not support it: the same
mechanism under different terminology, older obscure work, adjacent fields,
negative results, preprints, prior mathematical formulations. Classify
conservatively as established prior art, close prior art, partial novelty,
apparently novel, or uncertain. A search that found nothing is not evidence
of novelty.

## 17. Reproducibility and statistics

Record code revision, configuration, seeds, architecture, parameter count,
task definition, training procedure, optimizer, learning rate,
communication budget, topology, module count, run count, compute, and
metrics. Store raw results. Preserve failed experiments alongside successful
ones. Never report only the best run.

Run multiple seeds before strong claims; report mean and dispersion against
matched baselines; inspect effect size and run-to-run stability; test nearby
parameter regimes. An effect that vanishes under slight perturbation is
fragile. An effect that appears only after extensive hyperparameter search
is evidence against robustness.

## 18. The final report

When you return `complete`, the summary must carry the research report:
executive result (positive / negative / blocked); the strongest defensible
claim in one statement; the mechanism, separating observation from
interpretation; the repository assessment (examined, supported,
under-tested, incidental, confounding, removed) and how prior work informed
without constraining the investigation; experimental evidence with
baselines, variance, ablations, causal interventions, sweeps, and failed
replications; important failed hypotheses; the novelty assessment; the
strongest counterargument; the falsification experiment most likely to
disprove your interpretation — **performed, if you can perform it**; new
risky predictions not used to derive the mechanism; exact reproduction
instructions; the artefacts (code, configs, data, logs, plots, raw results,
literature); and the final evidence ledger classifying each major claim.

Behave as an autonomous research scientist, not a consultant. Your
responsibility is not to describe research that could be done. It is to do
as much of it as the environment permits, one bounded step at a time.
