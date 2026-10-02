# Video QA report: VoltSight_Hackathon_Explainer.mp4

| Property | Value |
|---|---|
| Duration | 4:48.9 (288.9 s), under the 5-minute target (the organisers' e-mail allows up to 10 minutes) |
| Container / codecs | MP4, H.264 High 1280×720 25 fps; AAC-LC mono 44.1 kHz |
| Decode check | `ffmpeg -v error -i … -f null -` completed without errors |
| Audio level | mean −15.8 dB, peak 0.0 dB (peaks reach full scale; no limiter was applied) |
| Narration | Piper neural TTS, voice model `en_US-ryan-high` (synthetic speech; no person's voice, no voice cloning); abbreviations spelled out phonetically in the script |
| Background music / sound effects | none |

## What is real
- Scenes 1, 4, 5, 6, 7 and the closing dashboard are Playwright recordings (1600×900, scaled to 1280×720) of the real VoltSight console running the live Compose profile with the simulator streaming (deterministic “range-risk” demo scenario). Alerts, map data, route and Copilot answer come from the running backend. A yellow dot is an overlay cursor; the top bar is an overlay caption stating it is a live recording.
- Scenes 2, 3, 8, 9 and the end card are slides built from repository content: the problem statement scale, the architecture figure, a Prometheus screenshot, the real EXPLAIN ANALYZE text, ZAP numbers, and the matrix counts and measured values.

## Checks performed
- Sampled frames at 22 points across the video and viewed them as contact sheets: no login form, no password, no `.env` content, no fake UI.
- Narration claims compared to evidence: 100K decisions/s framing is the problem statement's scale (not claimed as live); “forty thousand registered, eight hundred reporting live” matches the dashboard at capture; 98.3K ev/s for 60 s, zero loss, API p95 ≈ 27 ms, ZAP 58/0/3, matrix 28/28/2/2, FAIL for the 5 s alert target and “not run” items are stated explicitly; the Copilot is described as a stub provider.
- Segment timing: each browser scene was driven to the length of its narration (actions padded or cut to the audio), so narration and visuals are aligned by construction; scene 5 (map) actions were trimmed to its narration length.

## Known limitations
- I did not watch the whole video in real time; QA was by sampled frames, duration/codec probes and a decode check. Transition fades are 0.3 s.
- The basemap is **not** shown: the recording environment could not reach the OpenFreeMap tile host, so the map shows markers on a plain background with the on-screen note “basemap unavailable … markers only”. The narration says this.
- The recording environment is the author's sandbox, not the owner's Codespaces URL.
- Audio peaks at 0 dB; a loudness normalisation pass was not done.
