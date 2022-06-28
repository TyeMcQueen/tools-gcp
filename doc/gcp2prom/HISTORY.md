Release are available as Docker containers tagged tyemcq/gcp2prom.
https://hub.docker.com/r/tyemcq/gcp2prom

v0.3.3 - 2022-06-27

* Ignore metric values using different buckets than found on start-up.
* Fix several bugs with ExplicitBuckets for GCP Distribution metrics.

v0.3.2 - 2022-06-01

* Even leaner image (0 security findings).
* Add startup probe; can take minutes to find all of the metrics.
* Report when metrics get scraped for the first time.
* Warn if metrics don't get scraped after being ready for 5 minutes.
* Use newer go-lager to prevent failure getting GCP Project ID.

v0.3.1 - 2022-05-26

* Figure out GCP Project ID reliably.
* Handle stats for a LB request exceeding 500 seconds rather than crash.

v0.3.0 - 2022-05-08

* Much leaner image (fewer unused components with security findings).
* Ignore metrics lacking sample period instead of failing to start.

Prior releases were only used internally.
