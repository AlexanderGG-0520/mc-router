package com.alecofc.mcrouter.control.paper;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

final class ControlConfigTest {
    @Test
    void parsesWildcardListenAddress() {
        assertEquals(8082, ControlConfig.parseListen(":8082").getPort());
    }

    @Test
    void rejectsInvalidListenAddress() {
        assertThrows(IllegalArgumentException.class, () -> ControlConfig.parseListen("8082"));
    }
}
