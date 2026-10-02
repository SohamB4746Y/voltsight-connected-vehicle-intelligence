# ML results

Source: [`evidence/G8/ml_report.json`](../../evidence/G8/ml_report.json); model ADR: [ADR-007](../adr/007-model-serving.md); pipeline in `ml/`.

| Item | Value |
|---|---|
| Prediction target | Energy consumption per trip, kWh/km |
| Model | `HistGradientBoostingRegressor` (artifact `ml/model.joblib`, sha256 `aef157a2…61b26`, trained 2026-10-01) |
| Dataset | Simulated: 2,500 vehicles, 24,006 trips (2,000 train vehicles, 500 test vehicles). Labels come from the simulator physics |
| Baselines | fleet mean; model catalogue; per-vehicle history (best baseline) |

## Split methodology

- **A, vehicle hold-out:** 500 vehicles never seen in training (4,755 trips).
- **B, time hold-out:** later trips held out (4,813 trips, 1,252 vehicles).

## Leakage prevention

Vehicle sets are disjoint; features exclude the label; a vehicle's first trip uses the catalogue prior (equal to the catalogue baseline); payload, driver style and true state of health are hidden from the features; a shuffled-label model scores MAE 0.0515, worse than the real model.

## Metrics

| | GBM MAE / MAPE | Fleet mean | Catalogue | Vehicle history | 95% CI of GBM − best baseline (MAE) |
|---|---|---|---|---|---|
| A vehicle hold-out | **0.01736 / 7.17%** | 0.05188 / 23.38% | 0.03301 / 13.17% | 0.0274 / 11.52% | −0.01124 … −0.00883 |
| B time hold-out | **0.01718 / 6.60%** | 0.05663 / 22.45% | 0.03699 / 13.81% | 0.02489 / 9.86% | −0.00862 … −0.00680 |

The GBM is significantly better than the best baseline on both splits (CI excludes 0).

## Other batch models (tests, simulator truth)

Battery state of health from charging sessions: MAPE 0.5% (optimistic: the assumed efficiency equals the simulator's). Trip detection: F1 0.954. Range-risk detection vs simulator truth (stress scenario, 25 strandings): EWMA recall 100%, precision 0.29, median lead 53 min ([`evidence/G6/status.md`](../../evidence/G6/status.md)).

## Limitations

Simulated labels: absolute errors do not transfer to real fleets. Serving is a Python batch artifact; a Go evaluator was not built. No drift monitoring.
