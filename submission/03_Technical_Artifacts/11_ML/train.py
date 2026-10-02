"""Train and evaluate the trip-consumption model (kWh/km) against baselines.

Data: `go run ./cmd/vsmldata` (simulated fleet; features observable in production, label = simulator's true energy).
Evaluation: (A) vehicle-level hold-out (20% of vehicles never seen in training) and (B) time hold-out (later trips
of the training vehicles). Baselines: fleet mean, catalogue (WLTP) consumption, the vehicle's own earlier trips.
Intervals: paired bootstrap over test vehicles. Hidden simulator physics (payload, driver style, true state of
health) are NOT features; the model can only learn them through the vehicle's observed history.

Usage: python ml/train.py [--data ml/data/trips.csv] [--out evidence/G8]
"""
import argparse, hashlib, json, os, sys, time
import numpy as np, pandas as pd, joblib
from sklearn.ensemble import HistGradientBoostingRegressor

FEATURES = ["model_id", "nominal_kwh", "wltp_kwh_km", "age_years", "city_code", "start_hour", "km", "prior_mean", "prior_last", "n_prior"]
TARGET = "true_kwh_km"


def load(path):
    df = pd.read_csv(path)
    df["city_code"] = df["city"].astype("category").cat.codes
    return df


def mae(y, p): return float(np.mean(np.abs(y - p)))
def mape(y, p): return float(np.mean(np.abs(y - p) / np.maximum(y, 1e-6)))


def boot_diff(df, a, b, n=2000, seed=0):
    """Paired bootstrap over vehicles of mean(|err_a| - |err_b|): negative means a is better."""
    rng = np.random.default_rng(seed)
    g = df.assign(d=np.abs(df[TARGET] - df[a]) - np.abs(df[TARGET] - df[b])).groupby("vin")["d"].agg(["sum", "count"])
    s, c = g["sum"].to_numpy(), g["count"].to_numpy()
    out = []
    for _ in range(n):
        i = rng.integers(0, len(s), len(s))
        out.append(s[i].sum() / c[i].sum())
    return [float(np.percentile(out, 2.5)), float(np.mean(s.sum() / c.sum())), float(np.percentile(out, 97.5))]


def report(df, name, preds):
    res = {}
    for k, col in preds.items():
        res[k] = {"mae_kwh_per_km": round(mae(df[TARGET], df[col]), 5), "mape_pct": round(100 * mape(df[TARGET], df[col]), 2)}
    base = min((k for k in preds if k != "gbm"), key=lambda k: res[k]["mae_kwh_per_km"])
    res["best_baseline"] = base
    lo, mid, hi = boot_diff(df, preds["gbm"], preds[base])
    res["gbm_minus_best_baseline_mae_ci95"] = {"low": round(lo, 5), "mean": round(mid, 5), "high": round(hi, 5)}
    res["gbm_significantly_better"] = hi < 0
    res["test_trips"], res["test_vehicles"] = int(len(df)), int(df["vin"].nunique())
    return res


def fit(train):
    m = HistGradientBoostingRegressor(max_iter=300, learning_rate=0.06, max_depth=6, l2_regularization=1.0, random_state=0)
    m.fit(train[FEATURES], train[TARGET])
    return m


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", default="ml/data/trips.csv")
    ap.add_argument("--out", default="evidence/G8")
    ap.add_argument("--artifact", default="ml/model.joblib")
    a = ap.parse_args()
    df = load(a.data)
    df = df[(df[TARGET] > 0.02) & (df[TARGET] < 1.0)].reset_index(drop=True)

    # vehicle-level split: a vehicle is entirely in train or entirely in test
    vins = np.array(sorted(df["vin"].unique()))
    rng = np.random.default_rng(42)
    test_v = set(rng.choice(vins, size=int(0.2 * len(vins)), replace=False))
    is_test_v = df["vin"].isin(test_v)
    train, test_a = df[~is_test_v].copy(), df[is_test_v].copy()
    # time split among training vehicles: last 25% of the day
    cut = train["start_s"].quantile(0.75)
    train_b, test_b = train[train["start_s"] <= cut].copy(), train[train["start_s"] > cut].copy()

    out = {"data": {"trips": int(len(df)), "vehicles": int(len(vins)), "train_vehicles": int(len(vins) - len(test_v)), "test_vehicles": int(len(test_v))}}
    fleet_mean = train[TARGET].mean()
    for name, tr, te in (("A_vehicle_holdout", train, test_a), ("B_time_holdout", train_b, test_b)):
        m = fit(tr)
        te["gbm"] = m.predict(te[FEATURES])
        te["fleet_mean"] = tr[TARGET].mean()
        te["catalogue"] = te["wltp_kwh_km"]
        te["vehicle_history"] = np.where(te["n_prior"] > 0, te["prior_mean"], te["wltp_kwh_km"])
        out[name] = report(te, name, {"gbm": "gbm", "fleet_mean": "fleet_mean", "catalogue": "catalogue", "vehicle_history": "vehicle_history"})
        if name.startswith("A"):
            final_model = m

    # leakage / sanity checks
    chk = {}
    assert set(train["vin"]).isdisjoint(set(test_a["vin"])), "vehicle leakage"
    chk["vehicle_sets_disjoint"] = True
    chk["features_exclude_label"] = TARGET not in FEATURES
    first = df[df["n_prior"] == 0]
    chk["first_trip_prior_equals_catalogue"] = bool(np.allclose(first["prior_mean"], first["wltp_kwh_km"], atol=1e-3))
    shuffled = train.copy()
    shuffled[TARGET] = rng.permutation(shuffled[TARGET].to_numpy())
    ms = fit(shuffled)
    chk["shuffled_label_model_mae"] = round(mae(test_a[TARGET], ms.predict(test_a[FEATURES])), 5)
    chk["shuffled_label_model_is_worse_than_real"] = chk["shuffled_label_model_mae"] > out["A_vehicle_holdout"]["gbm"]["mae_kwh_per_km"] * 1.5
    out["checks"] = chk

    # final artefact: refit on all trips; version = hash of the artefact
    final_model = fit(df)
    joblib.dump({"model": final_model, "features": FEATURES, "city_categories": list(df["city"].astype("category").cat.categories)}, a.artifact)
    sha = hashlib.sha256(open(a.artifact, "rb").read()).hexdigest()
    out["artifact"] = {"path": a.artifact, "sha256": sha, "trained_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "algorithm": "HistGradientBoostingRegressor"}
    out["limitation"] = ("Simulated world: labels come from the simulator's physics; payload, driver style and true state of health are hidden from the "
                         "features. Absolute errors do not transfer to real fleets.")
    os.makedirs(a.out, exist_ok=True)
    json.dump(out, open(os.path.join(a.out, "ml_report.json"), "w"), indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    sys.exit(main())
