package com.alecofc.mcrouter.control.paper;

public enum BackendAvailability {
    ONLINE,
    OFFLINE;

    static BackendAvailability parse(String value) {
        return switch (value) {
            case "online" -> ONLINE;
            case "offline" -> OFFLINE;
            default -> throw new IllegalArgumentException("availability must be online or offline");
        };
    }
}
