package com.alecofc.mcrouter.control.paper;

import org.junit.jupiter.api.Test;

import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

final class BackendAvailabilityStoreTest {
    @Test
    void emitsOnlyOnAvailabilityTransition() {
        BackendAvailabilityStore store = new BackendAvailabilityStore();
        assertTrue(store.update("survival", BackendAvailability.ONLINE, Instant.now()).isPresent());
        assertFalse(store.update("survival", BackendAvailability.ONLINE, Instant.now()).isPresent());
        assertTrue(store.update("survival", BackendAvailability.OFFLINE, Instant.now()).isPresent());
    }
}
