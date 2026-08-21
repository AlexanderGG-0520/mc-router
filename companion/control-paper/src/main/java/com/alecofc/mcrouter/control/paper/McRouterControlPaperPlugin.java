package com.alecofc.mcrouter.control.paper;

import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.format.NamedTextColor;
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
        Bukkit.getScheduler().runTask(this, () -> store.update(backendId, availability, Instant.now()).ifPresent(change -> {
            Bukkit.getPluginManager().callEvent(new BackendAvailabilityChangeEvent(change.previous(), change.current()));
            Bukkit.broadcast(availabilityMessage(change.current()));
        }));
    }

    private static Component availabilityMessage(BackendStatus status) {
        boolean online = status.availability() == BackendAvailability.ONLINE;
        return Component.text("[mc-router] ", NamedTextColor.DARK_GRAY)
                .append(Component.text(status.backendId(), NamedTextColor.AQUA))
                .append(Component.text(online ? " が起動しました。" : " は現在停止中です。", online ? NamedTextColor.GREEN : NamedTextColor.RED));
    }
}
