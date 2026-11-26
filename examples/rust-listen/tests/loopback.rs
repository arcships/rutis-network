use link_listen_example::ListenerSessions;
use rutis::host::{CtxHost, FiberState, FiberView};
use rutis::{Ctx, Plugin};
use rutis_bridge::channel::Closer;
use rutis_bridge::session::{
    host_key, HostDispatch, Reply, SessionOptions, Value, ENDPOINT_PROTOCOL,
};
use rutis_bridge::websocket::{self, Admitted, Credential, Limits, ListenerConfig, Refused, Trust};
use rutis_bridge::{Close, LocalId, PeerId, Session};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::mpsc;

struct Beacon;

impl HostDispatch for Beacon {
    fn invoke(&self, _: &str, _: Value) -> Reply {
        Ok(Value::Undefined)
    }

    fn methods(&self) -> Option<serde_json::Value> {
        Some(serde_json::json!({ "ping": "async" }))
    }
}

impl Plugin for Beacon {
    type Deps = ();

    async fn apply(&self, ctx: &Ctx, _: ()) -> rutis::Result {
        ctx.provide_at(host_key("beacon"), Arc::new(Beacon))?;
        Ok(())
    }
}

struct ObservedClose {
    transport: Arc<dyn Closer>,
    reasons: Arc<Mutex<Vec<Close>>>,
}

impl Closer for ObservedClose {
    fn close(&self, why: Close) {
        self.reasons.lock().unwrap().push(why.clone());
        self.transport.close(why);
    }
}

fn node(id: &str) -> Ctx {
    let root = Ctx::root().unwrap();
    root.provide(LocalId(PeerId::new(id).unwrap())).unwrap();
    root
}

async fn disconnected(view: &FiberView) {
    view.watch()
        .wait_for(|s| matches!(s.state, FiberState::Disposed | FiberState::Failed))
        .await
        .unwrap();
}

#[tokio::test(flavor = "multi_thread")]
async fn admit_then_replace_only_the_same_greeted_id() {
    tokio::time::timeout(Duration::from_secs(20), exercise())
        .await
        .unwrap();
}

async fn exercise() {
    let mut secret = [0u8; 32];
    getrandom::getrandom(&mut secret).unwrap();
    let token: String = secret.iter().map(|b| format!("{b:02x}")).collect();
    let credential = Credential::Bearer(token.clone());
    let protocol = format!("rutis.{ENDPOINT_PROTOCOL}");
    let selected = protocol.clone();
    let (accepted, mut incoming) = mpsc::channel(4);
    let limits = Limits::default();
    let config = ListenerConfig::new("example", "127.0.0.1:0".parse().unwrap());
    let listener = websocket::listen(
        &config,
        &limits,
        Arc::new(move |request| {
            if request.bearer != Some(&token) {
                return Err(Refused::Forbidden("admission denied".into()));
            }
            let accepted = accepted.clone();
            Ok(Admitted {
                protocol: selected.clone(),
                deliver: Box::new(move |channel| {
                    if let Err(error) = accepted.try_send(channel) {
                        error.into_inner().closer.close(Close::Done);
                    }
                }),
            })
        }),
    )
    .unwrap();
    let address = format!("ws://{}/rutis", listener.local_addr());
    let trust = Trust::default();
    assert!(websocket::dial(&address, &protocol, None, &trust, &limits)
        .await
        .is_err());
    assert!(incoming.try_recv().is_err());

    let hub = node("hub");
    let mut sessions = ListenerSessions::default();
    let mut clients = Vec::new();
    let first_close = Arc::new(Mutex::new(Vec::new()));
    for name in ["east", "west", "east"] {
        let root = node(name);
        if name == "west" {
            root.plugin(Beacon).await.unwrap();
        }
        let channel = websocket::dial(&address, &protocol, Some(&credential), &trust, &limits)
            .await
            .unwrap();
        let options = SessionOptions {
            outbound_limit: Some(4 * 1024 * 1024),
        };
        let view = root.plugin(Session::new(channel).options(options));
        let mut admitted = incoming.recv().await.unwrap();
        if clients.is_empty() {
            admitted.closer = Arc::new(ObservedClose {
                transport: admitted.closer,
                reasons: first_close.clone(),
            });
        }
        assert_eq!(sessions.accept(&hub, admitted).await.unwrap(), name);
        (&view).await.unwrap();
        clients.push((root, view));
    }
    // The remote plugin carries the greeted ID, independent of the shared bearer.
    while !hub
        .diagnostics()
        .plugins
        .iter()
        .any(|p| p.name == "west:Beacon" && p.state == FiberState::Active)
    {
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    // An admitted connection that leaves before greeting cannot evict a live one.
    let incomplete = websocket::dial(&address, &protocol, Some(&credential), &trust, &limits)
        .await
        .unwrap();
    let admitted = incoming.recv().await.unwrap();
    incomplete.closer.close(Close::Done);
    assert!(sessions.accept(&hub, admitted).await.is_err());
    disconnected(&clients[0].1).await;
    assert_eq!(*first_close.lock().unwrap(), [Close::Replaced]);
    assert_eq!(clients[1].1.state().state, FiberState::Active);
    assert_eq!(clients[2].1.state().state, FiberState::Active);
    hub.shutdown().await.unwrap();
    for (root, _) in clients {
        root.shutdown().await.unwrap();
    }
}
