import { useEffect, useState } from "react";
import "./App.css";
import ContainerCard from "./components/ContainerCard";

function App() {
  const [containers, setContainers] = useState([
    { name: "immich", status: null, loading: true },
  ]);

  // Extract services from API response and add them to the container list
  const syncServicesFromStatus = async () => {
    const statusData = await fetchContainerStatus("immich");
    if (statusData.containers && Array.isArray(statusData.containers)) {
      const services = statusData.containers.map((container) => ({
        name: container.Service,
        status: { containers: [container], status: "success" },
        loading: false,
      }));
      setContainers(services);
    }
  };

  const fetchContainerStatus = async (containerName) => {
    try {
      const response = await fetch(`/api/docker/status/${containerName}`);
      const data = await response.json();
      return data;
    } catch (error) {
      console.error(`Error fetching status for ${containerName}:`, error);
      return { status: "error", message: error.message };
    }
  };

  const updateContainerStatuses = async () => {
    const updatedContainers = await Promise.all(
      containers.map(async (container) => {
        const statusData = await fetchContainerStatus(container.name);
        return {
          ...container,
          status: statusData,
          loading: false,
        };
      })
    );
    setContainers(updatedContainers);
  };

  useEffect(() => {
    syncServicesFromStatus();
  }, []);

  const handleAction = async (containerName, action) => {
    // Update loading state
    setContainers((prev) =>
      prev.map((c) => (c.name === containerName ? { ...c, loading: true } : c))
    );

    try {
      const response = await fetch(`/api/docker/${action}/${containerName}`);
      const data = await response.json();

      // Wait a bit for Docker to update
      setTimeout(() => {
        updateContainerStatuses();
      }, 2000);

      return data;
    } catch (error) {
      console.error(`Error performing ${action} on ${containerName}:`, error);
      setContainers((prev) =>
        prev.map((c) =>
          c.name === containerName ? { ...c, loading: false } : c
        )
      );
    }
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>🐳 Home Server Manager</h1>
        <p>Manage your Docker containers</p>
      </header>

      <main className="container-grid">
        {containers.map((container) => (
          <ContainerCard
            key={container.name}
            container={container}
            onAction={handleAction}
            onRefresh={updateContainerStatuses}
          />
        ))}
      </main>

      <section className="metrics-placeholder">
        <h2>📊 Host Metrics</h2>
        <p>Node Exporter metrics will be displayed here</p>
        <div className="metrics-grid">
          <div className="metric-card">
            <h3>CPU Usage</h3>
            <p>Coming soon...</p>
          </div>
          <div className="metric-card">
            <h3>Memory Usage</h3>
            <p>Coming soon...</p>
          </div>
          <div className="metric-card">
            <h3>Disk Usage</h3>
            <p>Coming soon...</p>
          </div>
        </div>
      </section>
    </div>
  );
}

export default App;
