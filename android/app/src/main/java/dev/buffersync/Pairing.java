package dev.buffersync;

import java.net.URI;
import java.util.HashMap;
import java.util.Map;

/** Strict parsing keeps credentials out of arbitrary URLs or HTTP redirects. */
final class Pairing {
    final String host, key, pin;
    final int port;
    private Pairing(String host, int port, String key, String pin) {
        this.host = host; this.port = port; this.key = key; this.pin = pin;
    }
    static Pairing parse(String link) {
        try {
            URI uri = new URI(link.trim());
            if (!"buffersync".equals(uri.getScheme()) || uri.getHost() == null
                    || uri.getUserInfo() != null || uri.getFragment() != null
                    || (uri.getPath() != null && !uri.getPath().isEmpty())
                    || uri.getPort() < 1 || uri.getPort() > 65535 || uri.getRawQuery() == null)
                throw new IllegalArgumentException();
            Map<String, String> query = new HashMap<>();
            for (String part : uri.getRawQuery().split("&")) {
                String[] pair = part.split("=", -1);
                if (pair.length != 2 || query.put(pair[0], pair[1]) != null)
                    throw new IllegalArgumentException();
            }
            String key = query.get("key"), pin = query.get("pin");
            if (query.size() != 2 || key == null || pin == null
                    || !key.matches("[a-f0-9]{64}") || !pin.matches("[a-f0-9]{64}"))
                throw new IllegalArgumentException();
            return new Pairing(uri.getHost(), uri.getPort(), key, pin);
        } catch (Exception e) {
            throw new IllegalArgumentException("Вставьте целую ссылку buffersync:// из программы на компьютере.");
        }
    }
}
