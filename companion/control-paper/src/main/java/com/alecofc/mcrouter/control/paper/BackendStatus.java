package com.alecofc.mcrouter.control.paper;

import java.time.Instant;

public record BackendStatus(String backendId, BackendAvailability availability, Instant observedAt) {}
