# Research guidance — template

Copy this to a project's guidance path and replace every `<...>`:

    .ariadne/research/<project>/AGENTS.md

That path is discovered automatically. To keep it somewhere else, point at
it explicitly:

    [research.projects.<project>]
    guidance_file = "docs/research/<project>.md"

Check what a project is actually loading with:

    ariadne research guidance <project>

A missing file is not an error — the prompt simply falls back to the
built-in sections alone.

---

## What this file is for

Guidance is the **durable mandate** for one research programme: the
question being answered, what counts as evidence, and the constraints the
work has to respect. It is loaded once per run and fenced into every Ari
and Astra prompt as reference material.

Ariadne already supplies the parts of the prompt that are the same for
every research project, and it supplies them *outside* this file:

| Already built in                    | Do not restate it here                        |
|-------------------------------------|-----------------------------------------------|
| Agent identity                      | Who Ari is, who Astra is, who may invoke whom |
| Scope notice                        | "Ignore GitHub issues / Linear tickets"       |
| Research context                    | Frontier, hypotheses, open questions, confounds |
| Escalation denial feedback          | Why the last escalation was refused           |
| User steering                       | Live instructions sent from the TUI           |
| Bounded-turn instruction            | "Perform exactly one bounded research step"   |
| Escalation rules                    | When escalation is a request, not a decision  |
| Output contract                     | The envelope JSON and every field in it       |

Two of those matter enough to spell out:

**Never assign identity here.** This file is fenced as reference material
precisely because it describes the whole team, and an Ari invocation that
took its identity from project prose is the exact failure this design
prevents. Writing "Astra is the lead researcher" is fine as description;
it will not, and must not, change who the reading agent is.

**Never restate the envelope format.** What is worth writing here is what
should *go into* those fields for this project — what counts as an
observation, what makes a hypothesis worth recording, what a confound
looks like in this domain.

Keep it short. It is prepended to every invocation, so length is a
recurring cost paid on every cycle.

---

# <Project name>

## Research question

<The one question this programme exists to answer. One or two sentences.
If you cannot state it in two sentences, the programme is really several
programmes and should be split.>

## Why it matters

<What becomes possible, or what decision gets unblocked, once this is
answered. This is what the agents weigh when reporting an escalation's
`importance`.>

## Success criteria

<Concretely, what would settle the question? Name the result that would
make you stop. A programme with no stopping condition never returns
`complete`.>

- <e.g. a preregistered effect replicates at n >= 3 with independent seeds>
- <e.g. the alternative explanation is ruled out by a discriminating test>

## Current state

<Where the programme stands today. Keep this to durable framing, not
findings: the live frontier, hypotheses, and rejections are injected
automatically from memory each cycle, and duplicating them here means
maintaining a second copy that will drift out of date.>

## What counts as evidence

<The standard a result must meet before it is worth recording as an
observation. This is the single highest-value section — it is what stops
a routine agent recording noise as findings.>

- <e.g. a result is not an observation until it survives a re-run with a
  fresh seed>
- <e.g. report effect sizes with variance, never a single run's number>
- <e.g. any comparison must name its matched baseline>

## Known confounds

<Traps specific to this domain that have already cost time. These reach
the agent as standing warnings, so it does not rediscover them.>

- <e.g. arms sharing a random seed produce a spurious advantage>

## Methods and constraints

<How the work must be done, and what it may not do.>

- <e.g. experiments run under `scripts/experiment.py`; do not add frameworks>
- <e.g. a single run must stay under N minutes; anything longer needs a
  smaller configuration first>
- <e.g. never modify recorded results in `results/`; append only>

## Dead ends

<Approaches already ruled out, and why. Rejected hypotheses come through
memory automatically; this section is for whole directions that are not
worth re-proposing.>

- <e.g. architecture search — tried in cycles 3-9, no signal, do not revisit>

## When escalation is genuinely warranted here

<The built-in rules already say escalation must beat routine work on
epistemic value. This section says what that means in this domain — what
a genuine causal fork looks like as opposed to ordinary uncertainty.>

- <e.g. two mechanisms both predict the observed result and no cheap
  experiment separates them>
- <e.g. NOT warranted: a run failed and needs debugging>
