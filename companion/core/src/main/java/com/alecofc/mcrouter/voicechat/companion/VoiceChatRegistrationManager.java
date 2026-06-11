package com.alecofc.mcrouter.voicechat.companion;

import java.io.Closeable;
import java.io.IOException;
import java.time.Duration;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.logging.Level;
import java.util.logging.Logger;

public final class VoiceChatRegistrationManager implements Closeable {
    private static final Logger LOGGER = Logger.getLogger(VoiceChatRegistrationManager.class.getName());

    private final CompanionConfig config;
    private final RegistrationHttpClient client;
    private final ScheduledExecutorService executor;
    private final Map<UUID, TrackedLease> leases;

    public VoiceChatRegistrationManager(CompanionConfig config) {
        this(config, new RegistrationHttpClient(config));
    }

    VoiceChatRegistrationManager(CompanionConfig config, RegistrationHttpClient client) {
        this.config = config;
        this.client = client;
        this.executor = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread thread = new Thread(r, "mc-router-voicechat-registration");
            thread.setDaemon(true);
            return thread;
        });
        this.leases = new ConcurrentHashMap<>();
    }

    public void registerBeforeUdp(UUID playerUuid) throws IOException, InterruptedException {
        if (!leases.containsKey(playerUuid) && leases.size() >= config.maxTrackedPlayers()) {
            throw new IOException("tracked player limit reached");
        }
        Lease lease = retry(() -> client.register(playerUuid));
        TrackedLease previous = leases.put(playerUuid, schedule(playerUuid, lease));
        if (previous != null) {
            previous.cancel();
            unregisterBestEffort(playerUuid, previous.lease());
        }
        LOGGER.fine(() -> "registered voicechat lease for player");
    }

    public void unregister(UUID playerUuid) {
        TrackedLease tracked = leases.remove(playerUuid);
        if (tracked == null) {
            return;
        }
        tracked.cancel();
        unregisterNowBestEffort(playerUuid, tracked.lease());
    }

    public int trackedPlayers() {
        return leases.size();
    }

    @Override
    public void close() {
        for (UUID playerUuid : leases.keySet()) {
            unregister(playerUuid);
        }
        executor.shutdownNow();
    }

    private TrackedLease schedule(UUID playerUuid, Lease lease) {
        ScheduledFuture<?> future = executor.scheduleAtFixedRate(
                () -> refresh(playerUuid),
                config.refreshInterval().toMillis(),
                config.refreshInterval().toMillis(),
                TimeUnit.MILLISECONDS
        );
        return new TrackedLease(lease, future);
    }

    private void refresh(UUID playerUuid) {
        TrackedLease tracked = leases.get(playerUuid);
        if (tracked == null) {
            return;
        }
        try {
            Lease refreshed = client.refresh(playerUuid, tracked.lease());
            TrackedLease replacement = new TrackedLease(refreshed, tracked.future());
            leases.replace(playerUuid, tracked, replacement);
        } catch (Exception e) {
            LOGGER.log(Level.WARNING, "voicechat registration refresh failed", e);
        }
    }

    private void unregisterBestEffort(UUID playerUuid, Lease lease) {
        executor.execute(() -> {
            unregisterNowBestEffort(playerUuid, lease);
        });
    }

    private void unregisterNowBestEffort(UUID playerUuid, Lease lease) {
        try {
            client.unregister(playerUuid, lease);
        } catch (Exception e) {
            LOGGER.log(Level.FINE, "voicechat unregister failed", e);
        }
    }

    private Lease retry(LeaseCall call) throws IOException, InterruptedException {
        IOException last = null;
        for (int attempt = 0; attempt < 3; attempt++) {
            try {
                return call.call();
            } catch (IOException e) {
                last = e;
                Thread.sleep(backoff(attempt).toMillis());
            }
        }
        throw last == null ? new IOException("registration failed") : last;
    }

    private static Duration backoff(int attempt) {
        return Duration.ofMillis(100L * (1L << attempt) + Math.floorMod(System.nanoTime(), 50));
    }

    private interface LeaseCall {
        Lease call() throws IOException, InterruptedException;
    }

    private record TrackedLease(Lease lease, ScheduledFuture<?> future) {
        void cancel() {
            future.cancel(false);
        }
    }
}
