# Privacy policy

dropo does not include advertising, analytics, telemetry or automatic crash
reporting. Profiles, subscription URLs, VPN credentials, settings and logs are
stored locally in the user's Windows or Android application data.

Network requests are made only to provide features requested by the user or
needed to operate the application:

- configured VPN, proxy and WireGuard endpoints receive the traffic routed to
  them according to the active profile;
- service health checks contact the service domains shown by the application;
- update checks contact the official dropo GitHub releases API and configured
  official download mirror;
- the optional network fingerprint check contacts `ipinfo.io` to determine the
  public country code;
- subscription import and refresh contact the URL supplied by the user.

The application does not send profiles, credentials, visited URLs or logs to
the dropo maintainers. Third-party endpoints are governed by their respective
privacy policies. Users can avoid optional checks by not invoking those
features, remove local application data after uninstall, and inspect all
network behavior in the source code in this repository.
