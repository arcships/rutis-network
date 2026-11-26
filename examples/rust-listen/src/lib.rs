//! Mount already-admitted channels. Each listener owns its own table.
use rutis::host::{FiberState, FiberView};
use rutis::Ctx;
use rutis_bridge::channel::Closer;
use rutis_bridge::{Channel, Close, Session};
use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

// Session disposal closes the channel after unloading its children. Preserve
// that ordering while telling transports that carry a reason about replacement.
struct ListenerClose {
    transport: Arc<dyn Closer>,
    replacing: AtomicBool,
    closed: AtomicBool,
}

impl Closer for ListenerClose {
    fn close(&self, reason: Close) {
        if self.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        let reason = if reason == Close::Done && self.replacing.load(Ordering::Acquire) {
            Close::Replaced
        } else {
            reason
        };
        self.transport.close(reason);
    }
}

#[derive(Default)]
pub struct ListenerSessions {
    active: HashMap<String, (FiberView, Arc<ListenerClose>)>,
}

impl ListenerSessions {
    /// This small example processes admitted channels serially.
    pub async fn accept(
        &mut self,
        ctx: &Ctx,
        mut channel: Channel,
    ) -> Result<String, Box<dyn std::error::Error>> {
        self.active.retain(|_, (view, _)| {
            !matches!(
                view.state().state,
                FiberState::Disposed | FiberState::Failed
            )
        });
        let closer = Arc::new(ListenerClose {
            transport: channel.closer,
            replacing: AtomicBool::new(false),
            closed: AtomicBool::new(false),
        });
        channel.closer = closer.clone();
        let greeted = Session::handshake(ctx, channel).await?;
        let id = greeted.id().to_owned();
        if let Some((old, closer)) = self.active.get(&id) {
            closer.replacing.store(true, Ordering::Release);
            if let Err(error) = old.dispose().await {
                closer.replacing.store(false, Ordering::Release);
                return Err(Box::new(error));
            }
        }
        let session = ctx.plugin(greeted);
        (&session).await?;
        self.active.insert(id.clone(), (session, closer));
        Ok(id)
    }
}
