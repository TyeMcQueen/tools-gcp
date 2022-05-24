Release are available as Docker containers tagged tyemcq/gcp2prom.

v0.3.1 - 2022-05-25

  * Figure out GCP Project ID reliably.
  * Handle stats for a LB request exceeding 500 seconds rather than crash.

v0.3.0 - 2022-05-08

  * Much leaner image (fewer unused components with security findings).
  * Ignore metrics lacking sample period instead of failing to start.

Prior releases were only used internally.
