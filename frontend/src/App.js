import { useEffect, useState } from "react";
import "./App.css";
import ContainerCard from "./components/ContainerCard";

function App() {
  const [services, setServices] = useState([]);
  const [containerStatus, setContainerStatus] = useState({});
  const [loading, setLoading] = useState(true);
  const [statusLoading, setStatusLoading] = useState(false);
  const [error, setError] = useState(null);

  useEffect(() => {
    const initializeApp = async () => {
      await fetchServices();
      await fetchContainerStatus();
    };
    initializeApp();
  }, []);

  const fetchServices = async () => {
    try {
      setLoading(true);
      const response = await fetch("/api/services");
      const data = await response.json();

      if (data.status === "success") {
        setServices(data.services);
        setError(null);
      } else {
        setError(data.message);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  };

  const fetchContainerStatus = async () => {
    try {
      setStatusLoading(true);
      const response = await fetch("/api/containers/status");
      const data = await response.json();

      if (data.status === "success") {
        // Create a map of service name to container data
        const statusMap = {};
        data.containers.forEach((container) => {
          statusMap[container.Service] = container;
        });
        setContainerStatus(statusMap);
      }
    } catch (err) {
      console.error("Error fetching container status:", err);
    } finally {
      setStatusLoading(false);
    }
  };

  const handleContainerAction = async (service, action) => {
    try {
      const response = await fetch(`/api/containers/${action}/${service}`, {
        method: "POST",
      });
      const data = await response.json();

      if (data.status === "success") {
        // Refresh status after action
        setTimeout(() => {
          fetchContainerStatus();
        }, 1000);
      } else {
        console.error(`Failed to ${action} ${service}:`, data.message);
        alert(`Failed to ${action} ${service}: ${data.message}`);
      }
    } catch (err) {
      console.error(`Error performing ${action} on ${service}:`, err);
      alert(`Error: ${err.message}`);
    }
  };

  if (loading) {
    return (
      <div className="App">
        <div className="loading">Loading services...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="App">
        <div className="error">Error: {error}</div>
      </div>
    );
  }

  return (
    <div className="App">
      <header className="App-header">
        <h1>🐳 Home Server Manager</h1>
        <p>Manage your Docker containers</p>
      </header>

      <div className="status-control">
        <button
          className="btn btn-refresh-status"
          onClick={fetchContainerStatus}
          disabled={statusLoading}
        >
          {statusLoading ? "Refreshing..." : "🔄 Refresh Status"}
        </button>
      </div>

      <main className="container-grid">
        {services.length === 0 ? (
          <p>No services found</p>
        ) : (
          services.map((service) => (
            <ContainerCard
              key={service}
              service={service}
              status={containerStatus[service]}
              onAction={handleContainerAction}
            />
          ))
        )}
      </main>
    </div>
  );
}

export default App;
