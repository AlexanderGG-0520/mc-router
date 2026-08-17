package com.alecofc.mcrouter.control.paper;

import org.bukkit.Bukkit;
import org.bukkit.plugin.java.JavaPlugin;

import java.io.IOException;
import java.time.Instant;

public final class McRouterControlPaperPlugin extends JavaPlugin {
    private final BackendAvailabilityStore store = new BackendAvailabilityStore();
    private ControlHttpServer server;

    @Override
    public void onEnable() {
        ControlConfig config = ControlConfig.fromEnvironment();
        try {
            server = new ControlHttpServer(config, this::update);
            server.start();
        } catch (IOException exception) {
            throw new IllegalStateException("start control HTTP server", exception);
        }
        getLogger().info("mc-router control companion listening on " + config.listen());
    }

    @Override
    public void onDisable() {
        if (server != null) {
            try {
                server.close();
            } catch (IOException exception) {
                getLogger().warning("stop control HTTP server: " + exception.getMessage());
            }
        }
    }

    private void update(String backendId, BackendAvailability availability) {
        store.update(backendId, availability, Instant.now()).ifPresent(change ->
                Bukkit.getScheduler().runTask(this, () -> Bukkit.getPluginManager().callEvent(
                        new BackendAvailabilityChangeEvent(change.previous(), change.current())
                ))
        );
    }
}
