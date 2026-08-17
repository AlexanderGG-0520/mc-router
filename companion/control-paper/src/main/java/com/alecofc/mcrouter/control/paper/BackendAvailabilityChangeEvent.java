package com.alecofc.mcrouter.control.paper;

import org.bukkit.event.Event;
import org.bukkit.event.HandlerList;

public final class BackendAvailabilityChangeEvent extends Event {
    private static final HandlerList HANDLERS = new HandlerList();
    private final BackendStatus previous;
    private final BackendStatus current;

    public BackendAvailabilityChangeEvent(BackendStatus previous, BackendStatus current) {
        this.previous = previous;
        this.current = current;
    }

    public BackendStatus previous() { return previous; }
    public BackendStatus current() { return current; }

    @Override
    public HandlerList getHandlers() { return HANDLERS; }

    public static HandlerList getHandlerList() { return HANDLERS; }
}
