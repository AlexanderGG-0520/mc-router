package com.alecofc.mcrouter.control.paper;

import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

final class BackendAvailabilityStore {
    private final Map<String, BackendStatus> statuses = new ConcurrentHashMap<>();

    Optional<Change> update(String backendId, BackendAvailability availability, Instant observedAt) {
        BackendStatus next = new BackendStatus(backendId, availability, observedAt);
        BackendStatus previous = statuses.put(backendId, next);
        if (previous != null && previous.availability() == availability) {
            return Optional.empty();
        }
        return Optional.of(new Change(previous, next));
    }

    record Change(BackendStatus previous, BackendStatus current) {}
}
